package pluginserver

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
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

// ServerOptions contains only host-approved, nonsecret launch parameters.
type ServerOptions struct {
	ConnectionID string
	Targets      []pluginapi.Target
}
type ServerDefinition struct {
	ID, Version  string
	Capabilities []string
}

func (d ServerDefinition) Manifest(sha string) pluginapi.Manifest {
	return pluginapi.Manifest{Schema: 1, ID: d.ID, Version: d.Version, Protocol: pluginapi.ServerProtocol, CoreMin: "0.6.0", CoreMaxExclusive: "0.7.0", Capabilities: d.Capabilities, Executable: "plugin", SHA256: sha}
}

// RunServer supplies the bounded socket server and hashes the running inode.
// The factory alone reads this worker's credentials; Core never receives them.
func RunServer(d ServerDefinition, factory func(ServerOptions) (serverv1.Processor, error)) error {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "Unix socket")
	client := flags.Uint("client-uid", 0, "Core UID")
	connection := flags.String("connection-id", "", "Connection slot")
	targets := flags.String("targets", "[]", "Approved destinations")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 || os.Geteuid() == 0 || *client == 0 || *client > uint(^uint32(0)) || *client == uint(os.Geteuid()) || !pluginapi.ValidConnectionID(*connection) || *socket != "/run/aster-server/"+*connection+"/plugin.sock" {
		return pluginapi.ErrInvalid
	}
	options := ServerOptions{ConnectionID: *connection}
	if pluginapi.DecodeStrict([]byte(*targets), pluginapi.MaxPolicyBytes, &options.Targets) != nil || len(options.Targets) > 8 {
		return pluginapi.ErrInvalid
	}
	processor, err := factory(options)
	if err != nil {
		return serverv1.ErrUnavailable
	}
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return serverv1.ErrUnavailable
	}
	defer self.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(self, (64<<20)+1))
	if err != nil || n > 64<<20 {
		return serverv1.ErrUnavailable
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
		return serverv1.ErrUnavailable
	}
	defer listener.Close()
	if os.Chmod(*socket, 0660) != nil {
		return serverv1.ErrUnavailable
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	return (ServerServer{Manifest: manifest, ConnectionID: *connection, ClientUID: uint32(*client), Processor: processor}).Serve(ctx, listener)
}

// ReadServerCredential reads one bounded regular file from systemd's private
// directory. LoadCredential=server:/etc/aster/server/<slot>/ prefixes filenames
// with server_: config.json becomes server_config.json.
func ReadServerCredential(name string) ([]byte, error) {
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if !strings.HasPrefix(dir, "/run/credentials/") || filepath.Clean(dir) != dir || strings.ContainsAny(name, "/\\") || !strings.HasPrefix(name, "server_") {
		return nil, pluginapi.ErrInvalid
	}
	fd, err := syscall.Open(filepath.Join(dir, name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, serverv1.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > 64<<10 || stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 || stat.Mode&0007 != 0 {
		return nil, pluginapi.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || int64(len(raw)) != stat.Size {
		return nil, serverv1.ErrUnavailable
	}
	return raw, nil
}
