package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrUnavailable = errors.New("runtime plugin unavailable")
var ErrIncompatible = errors.New("runtime plugin incompatible")
var ErrDisabled = errors.New("runtime plugin disabled")

// trust is fixed to root in production. Tests can use private unprivileged
// fixtures without weakening the exported constructor or environment contract.
type trust struct {
	owner, systemRoot uint32
	allowSameUID      bool
}

func cleanAbsolute(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 1024 {
		return false
	}
	for _, c := range path {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func unchanged(a, b syscall.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Nlink == b.Nlink && a.Uid == b.Uid && a.Gid == b.Gid && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

// Walk every component using directory descriptors. No symlink is followed,
// even when it would remain inside the nominal plugin directory.
func protectedOpen(path string, directory bool, rules trust) (*os.File, error) {
	if !cleanAbsolute(path) || path == "/" {
		return nil, ErrUnavailable
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		isDir := directory || i < len(parts)-1
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if isDir {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return nil, ErrUnavailable
		}
		fd = next
		var info syscall.Stat_t
		if syscall.Fstat(fd, &info) != nil {
			_ = syscall.Close(fd)
			return nil, ErrUnavailable
		}
		validOwner := info.Uid == rules.owner
		if i < len(parts)-1 {
			validOwner = validOwner || info.Uid == rules.systemRoot
		}
		stickyParent := i < len(parts)-1 && isDir && info.Mode&syscall.S_ISVTX != 0 && info.Uid == rules.systemRoot
		if !validOwner || info.Mode&0022 != 0 && !stickyParent || info.Mode&(syscall.S_ISUID|syscall.S_ISGID) != 0 ||
			isDir && info.Mode&syscall.S_IFMT != syscall.S_IFDIR || !isDir && (info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1) {
			_ = syscall.Close(fd)
			return nil, ErrUnavailable
		}
	}
	return os.NewFile(uintptr(fd), "approved-plugin-input"), nil
}

func readProtected(path string, limit int64, rules trust) ([]byte, error) {
	file, err := protectedOpen(path, false, rules)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var before, after syscall.Stat_t
	if syscall.Fstat(int(file.Fd()), &before) != nil || before.Size <= 0 || before.Size > limit {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) != before.Size || syscall.Fstat(int(file.Fd()), &after) != nil || !unchanged(before, after) {
		return nil, ErrUnavailable
	}
	return raw, nil
}

type Approval struct {
	Manifest       pluginapi.Manifest
	Selection      pluginapi.Selection
	Executable     *os.File
	PolicyRevision uint64
}

func (a *Approval) Close() {
	if a != nil && a.Executable != nil {
		_ = a.Executable.Close()
	}
}

func selectionFor(policy pluginapi.Policy, kind string) *pluginapi.Selection {
	if strings.HasPrefix(kind, "server:") {
		for i := range policy.Servers {
			if policy.Servers[i].Slot == strings.TrimPrefix(kind, "server:") {
				return &policy.Servers[i]
			}
		}
		return nil
	}

	if strings.HasPrefix(kind, "payment:") {
		for i := range policy.Payments {
			if policy.Payments[i].Slot == strings.TrimPrefix(kind, "payment:") {
				return &policy.Payments[i]
			}
		}
		return nil
	}
	if kind == "payment" {
		return policy.Payment
	}
	return policy.SMTP
}
func validateSelection(s *pluginapi.Selection, kind string, rules trust) error {
	if s == nil {
		return nil
	}
	prefix := "email."
	if kind == "payment" {
		prefix = "payment."
	}
	if kind == "server" {
		prefix = "server."
	}
	if !pluginapi.ValidID(s.ID) || !strings.HasPrefix(s.ID, prefix) || !pluginapi.ValidVersion(s.Version) || !pluginapi.ValidDigest(s.ManifestSHA256) || !cleanAbsolute(s.Socket) || len(s.Socket) > 100 || s.WorkerUID == 0 || s.ClientUID == 0 || (!rules.allowSameUID && s.WorkerUID == s.ClientUID) || len(s.AllowedTargets) > 8 || kind != "payment" && kind != "server" && s.Enabled && len(s.AllowedTargets) == 0 {
		return ErrUnavailable
	}
	if kind == "payment" && (s.ID == "payment.disabled" || !paymentv1.ValidID(s.MerchantID)) || kind == "smtp" && s.MerchantID != "" {
		return ErrUnavailable
	}
	seen := map[pluginapi.Target]bool{}
	for _, target := range s.AllowedTargets {
		if target.Host == "" || len(target.Host) > 253 || strings.ContainsAny(target.Host, "/\\\x00\r\n\t ") || target.Port < 1 || target.Port > 65535 || seen[target] {
			return ErrUnavailable
		}
		seen[target] = true
	}
	return nil
}
func validatePolicyKind(policy pluginapi.Policy, kind string, rules trust) error {
	if policy.Schema != 1 || policy.Revision == 0 || kind != "smtp" && kind != "payment" && !(strings.HasPrefix(kind, "payment:") && pluginapi.ValidGatewayID(strings.TrimPrefix(kind, "payment:"))) && !(strings.HasPrefix(kind, "server:") && pluginapi.ValidConnectionID(strings.TrimPrefix(kind, "server:"))) {
		return ErrUnavailable
	}
	if validateSelection(policy.SMTP, "smtp", rules) != nil || validateSelection(policy.Payment, "payment", rules) != nil {
		return ErrUnavailable
	}
	if len(policy.Payments) > 64 || policy.Payment != nil && len(policy.Payments) > 0 {
		return ErrUnavailable
	}
	all := []*pluginapi.Selection{policy.SMTP, policy.Payment}
	for _, selected := range all {
		if selected != nil && selected.Slot != "" {
			return ErrUnavailable
		}
	}
	seen := map[string]bool{}
	for i := range policy.Payments {
		p := &policy.Payments[i]
		if !pluginapi.ValidGatewayID(p.Slot) || seen[p.Slot] || p.Socket != "/run/aster-payment/"+p.Slot+"/plugin.sock" || validateSelection(p, "payment", rules) != nil {
			return ErrUnavailable
		}
		seen[p.Slot] = true
		all = append(all, p)
	}
	if len(policy.Servers) > 64 {
		return ErrUnavailable
	}
	serverSlots := map[string]bool{}
	for i := range policy.Servers {
		p := &policy.Servers[i]
		if !pluginapi.ValidConnectionID(p.Slot) || serverSlots[p.Slot] || p.Socket != "/run/aster-server/"+p.Slot+"/plugin.sock" || p.MerchantID != "" || validateSelection(p, "server", rules) != nil {
			return ErrUnavailable
		}
		serverSlots[p.Slot] = true
		all = append(all, p)
	}
	for i, left := range all {
		for _, right := range all[i+1:] {
			if left != nil && right != nil && (left.Socket == right.Socket || !rules.allowSameUID && (left.WorkerUID == right.WorkerUID || left.WorkerUID == right.ClientUID || right.WorkerUID == left.ClientUID)) {
				return ErrUnavailable
			}
		}
	}
	if s := selectionFor(policy, kind); s == nil || !s.Enabled {
		return ErrDisabled
	}
	return nil
}
func validatePolicy(policy pluginapi.Policy, rules trust) error {
	return validatePolicyKind(policy, "smtp", rules)
}

func loadApproval(root, policyPath string, rules trust) (*Approval, error) {
	return loadApprovalKind(root, policyPath, "smtp", rules)
}
func loadApprovalKind(root, policyPath, kind string, rules trust, inspectDisabled ...bool) (*Approval, error) {
	if root == "" && policyPath == "" {
		return nil, ErrDisabled
	}
	if !cleanAbsolute(root) || !cleanAbsolute(policyPath) {
		return nil, ErrUnavailable
	}
	rootFile, err := protectedOpen(root, true, rules)
	if err != nil {
		return nil, err
	}
	rootFile.Close()
	raw, err := readProtected(policyPath, pluginapi.MaxPolicyBytes, rules)
	if err != nil {
		return nil, err
	}
	var policy pluginapi.Policy
	if pluginapi.DecodeStrict(raw, pluginapi.MaxPolicyBytes, &policy) != nil {
		return nil, ErrUnavailable
	}
	if err := validatePolicyKind(policy, kind, rules); err != nil && !(errors.Is(err, ErrDisabled) && len(inspectDisabled) > 0 && inspectDisabled[0] && selectionFor(policy, kind) != nil) {
		// Disabled policy metadata is useful for version/update status, but it
		// grants no executable approval and does not read the selected binary.
		if errors.Is(err, ErrDisabled) && selectionFor(policy, kind) != nil {
			return &Approval{Selection: *selectionFor(policy, kind), PolicyRevision: policy.Revision}, err
		}
		return nil, err
	}
	s := *selectionFor(policy, kind)
	if strings.HasPrefix(kind, "payment:") {
		kind = "payment"
	}
	if strings.HasPrefix(kind, "server:") {
		kind = "server"
	}
	base := filepath.Join(root, s.ID, s.Version)
	raw, err = readProtected(filepath.Join(base, "manifest.json"), pluginapi.MaxManifestBytes, rules)
	if err != nil {
		return nil, err
	}
	manifestHash := sha256.Sum256(raw)
	if hex.EncodeToString(manifestHash[:]) != s.ManifestSHA256 {
		return nil, ErrUnavailable
	}
	var manifest pluginapi.Manifest
	if pluginapi.DecodeStrict(raw, pluginapi.MaxManifestBytes, &manifest) != nil || manifest.ID != s.ID || manifest.Version != s.Version || kind == "payment" && manifest.Protocol != pluginapi.PaymentProtocol || kind == "smtp" && manifest.Protocol != pluginapi.SMTPProtocol || kind == "server" && manifest.Protocol != pluginapi.ServerProtocol {
		return nil, ErrUnavailable
	}
	if manifest.Validate(pluginapi.CoreVersion) != nil {
		return &Approval{Manifest: manifest, Selection: s, PolicyRevision: policy.Revision}, ErrIncompatible
	}
	file, err := protectedOpen(filepath.Join(base, manifest.Executable), false, rules)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			file.Close()
		}
	}()
	var before, after syscall.Stat_t
	if syscall.Fstat(int(file.Fd()), &before) != nil || before.Size < 64 || before.Size > 64<<20 || before.Mode&0111 == 0 {
		return nil, ErrUnavailable
	}
	header := make([]byte, 20)
	if _, err = io.ReadFull(file, header); err != nil || string(header[:4]) != "\x7fELF" || header[4] != 2 || header[5] != 1 || header[18] != 62 || header[19] != 0 {
		return nil, ErrUnavailable
	}
	if _, err = file.Seek(0, 0); err != nil {
		return nil, ErrUnavailable
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, (64<<20)+1))
	if err != nil || n != before.Size || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 || syscall.Fstat(int(file.Fd()), &after) != nil || !unchanged(before, after) {
		return nil, ErrUnavailable
	}
	if _, err = file.Seek(0, 0); err != nil {
		return nil, ErrUnavailable
	}
	valid = true
	return &Approval{Manifest: manifest, Selection: s, Executable: file, PolicyRevision: policy.Revision}, nil
}

// OpenApproved is shared by the nonprivileged API and the separate systemd
// launcher. Only root-owned approved artifacts are accepted in production.
func OpenApproved(root, policyPath string) (*Approval, error) {
	return loadApproval(root, policyPath, trust{})
}

// OpenApprovedPayment pins the independently selected payment worker without
// executing it. The launcher applies the same approval checks as the core.
func OpenApprovedPayment(root, policyPath string, slot ...string) (*Approval, error) {
	kind := "payment"
	if len(slot) > 0 && slot[0] != "" {
		kind += ":" + slot[0]
	}
	return loadApprovalKind(root, policyPath, kind, trust{})
}

func OpenApprovedServer(root, policyPath, slot string) (*Approval, error) {
	return loadApprovalKind(root, policyPath, "server:"+slot, trust{})
}
