package pluginserver

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// PaymentOptions contains only host-approved, nonsecret launch parameters.
type PaymentOptions struct {
	GatewayID, MerchantID string
	Targets               []pluginapi.Target
}
type PaymentDefinition struct {
	ID, Version  string
	Declarations paymentv1.Declarations
	Capabilities []string
}

func (d PaymentDefinition) Manifest(sha string) pluginapi.Manifest {
	return pluginapi.Manifest{Schema: 1, ID: d.ID, Version: d.Version, Protocol: pluginapi.PaymentProtocol, CoreMin: "0.6.0", CoreMaxExclusive: "0.7.0", Capabilities: d.Capabilities, Executable: "plugin", SHA256: sha, Payment: &d.Declarations}
}

// RunPayment supplies the bounded socket server and hashes the running inode.
// The factory alone reads this worker's credentials; Core never receives them.
func RunPayment(d PaymentDefinition, factory func(PaymentOptions) (paymentv1.Processor, error)) error {
	flags := flag.NewFlagSet("payment", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "Unix socket")
	client := flags.Uint("client-uid", 0, "Core UID")
	gateway := flags.String("gateway-id", "", "Gateway slot")
	merchant := flags.String("merchant-id", "", "Merchant identity")
	targets := flags.String("targets", "[]", "Approved destinations")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 || os.Geteuid() == 0 || *client == 0 || *client > uint(^uint32(0)) || *client == uint(os.Geteuid()) || !paymentv1.ValidGatewayID(*gateway) || !paymentv1.ValidID(*merchant) || *socket != "/run/aster-payment/"+*gateway+"/plugin.sock" {
		return pluginapi.ErrInvalid
	}
	options := PaymentOptions{GatewayID: *gateway, MerchantID: *merchant}
	if pluginapi.DecodeStrict([]byte(*targets), pluginapi.MaxPolicyBytes, &options.Targets) != nil || len(options.Targets) > 8 {
		return pluginapi.ErrInvalid
	}
	processor, err := factory(options)
	if err != nil {
		return paymentv1.ErrUnavailable
	}
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return paymentv1.ErrUnavailable
	}
	defer self.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(self, (64<<20)+1))
	if err != nil || n > 64<<20 {
		return paymentv1.ErrUnavailable
	}
	manifest := d.Manifest(hex.EncodeToString(hash.Sum(nil)))
	if manifest.Validate(pluginapi.CoreVersion) != nil {
		return pluginapi.ErrInvalid
	}
	// RuntimeDirectory belongs exclusively to this service and is cleared on stop.
	info, err := os.Lstat(filepath.Dir(*socket))
	if err != nil || !info.IsDir() || info.Mode()&0022 != 0 {
		return pluginapi.ErrInvalid
	}
	if _, err = os.Lstat(*socket); err == nil {
		return pluginapi.ErrInvalid
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: *socket, Net: "unix"})
	if err != nil {
		return paymentv1.ErrUnavailable
	}
	defer listener.Close()
	if os.Chmod(*socket, 0660) != nil {
		return paymentv1.ErrUnavailable
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	return (PaymentServer{Manifest: manifest, GatewayID: *gateway, MerchantID: *merchant, ClientUID: uint32(*client), Processor: processor}).Serve(ctx, listener)
}

// ReadPaymentCredential reads one bounded regular file from systemd's private
// directory. LoadCredential=payment:/etc/aster/payment/<slot>/ prefixes filenames
// with payment_: config.json becomes payment_config.json.
func ReadPaymentCredential(name string) ([]byte, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if !strings.HasPrefix(dir, "/run/credentials/") || filepath.Clean(dir) != dir || strings.ContainsAny(name, "/\\") || !strings.HasPrefix(name, "payment_") {
		return nil, pluginapi.ErrInvalid
	}
	fd, err := syscall.Open(filepath.Join(dir, name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, paymentv1.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > 64<<10 || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 || stat.Mode&0007 != 0 {
		return nil, pluginapi.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || int64(len(raw)) != stat.Size {
		return nil, paymentv1.ErrUnavailable
	}
	return raw, nil
}
