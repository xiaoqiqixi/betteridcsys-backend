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
	if kind == "payment" {
		return policy.Payment
	}
	if kind == "resource" {
		return policy.Resource
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
	if kind == "resource" {
		prefix = "resource."
	}
	if !pluginapi.ValidID(s.ID) || !strings.HasPrefix(s.ID, prefix) || !pluginapi.ValidVersion(s.Version) || !pluginapi.ValidDigest(s.ManifestSHA256) || !cleanAbsolute(s.Socket) || len(s.Socket) > 100 || s.WorkerUID == 0 || kind != "resource" && s.ClientUID == 0 || (!rules.allowSameUID && s.WorkerUID == s.ClientUID) || len(s.AllowedTargets) > 8 || s.Enabled && len(s.AllowedTargets) == 0 {
		return ErrUnavailable
	}
	if kind == "resource" {
		if (!rules.allowSameUID && s.ClientUID != 0) || !pluginapi.ValidControllerID(s.ControllerID) || s.MerchantID != "" {
			return ErrUnavailable
		}
	} else if s.ControllerID != "" {
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
	if policy.Schema != 1 || policy.Revision == 0 || kind != "smtp" && kind != "payment" && kind != "resource" {
		return ErrUnavailable
	}
	if validateSelection(policy.SMTP, "smtp", rules) != nil || validateSelection(policy.Payment, "payment", rules) != nil || validateSelection(policy.Resource, "resource", rules) != nil {
		return ErrUnavailable
	}
	if a, b := policy.SMTP, policy.Payment; a != nil && b != nil && (a.Socket == b.Socket || !rules.allowSameUID && (a.WorkerUID == b.WorkerUID || a.WorkerUID == b.ClientUID || b.WorkerUID == a.ClientUID)) {
		return ErrUnavailable
	}
	for _, other := range []*pluginapi.Selection{policy.SMTP, policy.Payment} {
		if r := policy.Resource; r != nil && other != nil && (r.Socket == other.Socket || !rules.allowSameUID && (r.WorkerUID == other.WorkerUID || r.WorkerUID == other.ClientUID || other.WorkerUID == r.ClientUID)) {
			return ErrUnavailable
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
func loadApprovalKind(root, policyPath, kind string, rules trust) (*Approval, error) {
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
	if err := validatePolicyKind(policy, kind, rules); err != nil {
		// Disabled policy metadata is useful for version/update status, but it
		// grants no executable approval and does not read the selected binary.
		if errors.Is(err, ErrDisabled) && selectionFor(policy, kind) != nil {
			return &Approval{Selection: *selectionFor(policy, kind), PolicyRevision: policy.Revision}, err
		}
		return nil, err
	}
	s := *selectionFor(policy, kind)
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
	if pluginapi.DecodeStrict(raw, pluginapi.MaxManifestBytes, &manifest) != nil || manifest.Validate(pluginapi.CoreVersion) != nil || manifest.ID != s.ID || manifest.Version != s.Version || kind == "payment" && manifest.Protocol != pluginapi.PaymentProtocol || kind == "smtp" && manifest.Protocol != pluginapi.SMTPProtocol || kind == "resource" && manifest.Protocol != pluginapi.ResourceProtocol {
		return nil, ErrUnavailable
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
func OpenApprovedPayment(root, policyPath string) (*Approval, error) {
	return loadApprovalKind(root, policyPath, "payment", trust{})
}

// OpenApprovedResource is the independent host-only resource worker slot.
func OpenApprovedResource(root, policyPath string) (*Approval, error) {
	return loadApprovalKind(root, policyPath, "resource", trust{})
}
