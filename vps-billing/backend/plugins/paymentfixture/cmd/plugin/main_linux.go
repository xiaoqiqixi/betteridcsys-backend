// This command is a synthetic development fixture, never a payment channel.
package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/paymentv1"
	"aster/billing/pkg/pluginserver"
	"aster/billing/plugins/paymentfixture"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var version = "0.1.0"

func main() {
	showVersion := flag.Bool("version", false, "Print synthetic fixture version")
	socket := flag.String("socket", "", "Private Unix socket")
	clientUID := flag.Uint("client-uid", 0, "Allowed core UID")
	merchantID := flag.String("merchant-id", "", "Approved synthetic merchant identity")
	targetsJSON := flag.String("targets", "[]", "Approved nonsecret destinations (fixture never connects)")
	flag.Parse()
	if flag.NArg() != 0 || !pluginapi.ValidVersion(version) {
		stop()
	}
	if *showVersion {
		fmt.Println("Aster synthetic payment fixture " + version)
		return
	}
	if os.Geteuid() == 0 || *clientUID == 0 || *clientUID > uint(^uint32(0)) || *clientUID == uint(os.Geteuid()) || !paymentv1.ValidID(*merchantID) {
		stop()
	}
	var targets []pluginapi.Target
	if pluginapi.DecodeStrict([]byte(*targetsJSON), pluginapi.MaxPolicyBytes, &targets) != nil || len(targets) > 8 {
		stop()
	}
	for _, target := range targets {
		if target.Host == "" || len(target.Host) > 253 || strings.ContainsAny(target.Host, "/\\\x00\r\n\t ") || target.Port < 1 || target.Port > 65535 {
			stop()
		}
	}
	secret, err := fixtureKey(os.Getenv("CREDENTIALS_DIRECTORY"))
	if err != nil {
		stop()
	}
	processor, err := paymentfixture.New(secret)
	clear(secret)
	if err != nil {
		stop()
	}
	// Hash the inode of this running process, including during an independent
	// immutable plugin update. A replacement on disk cannot change its identity.
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		stop()
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(self, (64<<20)+1))
	self.Close()
	if err != nil || size < 64 || size > 64<<20 {
		stop()
	}
	declarations := paymentfixture.Declarations()
	manifest := pluginapi.Manifest{
		Schema: 1, ID: paymentfixture.ProviderID, Version: version,
		Protocol: pluginapi.PaymentProtocol, CoreMin: "0.5.3", CoreMaxExclusive: "0.6.0",
		Capabilities: pluginapi.PaymentCapabilities(), Executable: "plugin", SHA256: hex.EncodeToString(hash.Sum(nil)),
		Payment: &declarations,
	}
	listener, err := listen(*socket)
	if err != nil {
		stop()
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	server := pluginserver.PaymentServer{Manifest: manifest, ClientUID: uint32(*clientUID), MerchantID: *merchantID, Processor: processor}
	if server.Serve(ctx, listener) != nil {
		stop()
	}
}

// fixtureKey accepts only the fixed, private systemd credential file. Keys are
// never passed as arguments, returned in errors, or read from arbitrary paths.
func fixtureKey(directory string) ([]byte, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || len(directory) > 4096 || strings.ContainsAny(directory, "\x00\r\n") {
		return nil, pluginapi.ErrInvalid
	}
	path := filepath.Join(directory, "payment-fixture-key")
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, pluginapi.ErrInvalid
	}
	file := os.NewFile(uintptr(fd), "payment-fixture-key")
	defer file.Close()
	var info syscall.Stat_t
	if syscall.Fstat(fd, &info) != nil || info.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		info.Nlink != 1 || info.Uid != 0 && info.Uid != uint32(os.Geteuid()) || info.Size < 32 || info.Size > 4096 {
		return nil, pluginapi.ErrInvalid
	}
	if !fixtureKeyAccess(fd, info, uint32(os.Geteuid())) {
		return nil, pluginapi.ErrInvalid
	}
	secret, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(secret) < 32 || len(secret) > 4096 {
		clear(secret)
		return nil, pluginapi.ErrInvalid
	}
	return secret, nil
}

func listen(path string) (*net.UnixListener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 100 {
		return nil, pluginapi.ErrInvalid
	}
	for _, c := range path {
		if c < 32 || c == 127 {
			return nil, pluginapi.ErrInvalid
		}
	}
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		var info syscall.Stat_t
		if syscall.Lstat(current, &info) != nil || info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Mode&0022 != 0 || info.Uid != 0 && info.Uid != uint32(os.Geteuid()) {
			return nil, pluginapi.ErrInvalid
		}
		if current == "/" {
			break
		}
	}
	var previous syscall.Stat_t
	if err := syscall.Lstat(path, &previous); err == nil {
		if previous.Mode&syscall.S_IFMT != syscall.S_IFSOCK || previous.Uid != uint32(os.Geteuid()) || previous.Nlink != 1 {
			return nil, pluginapi.ErrInvalid
		}
		connection, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			connection.Close()
			return nil, pluginapi.ErrInvalid
		}
		if !errorsIsConnectionRefused(err) {
			return nil, pluginapi.ErrInvalid
		}
		var current syscall.Stat_t
		if syscall.Lstat(path, &current) != nil || current.Dev != previous.Dev || current.Ino != previous.Ino || current.Uid != previous.Uid || current.Mode != previous.Mode {
			return nil, pluginapi.ErrInvalid
		}
		if os.Remove(path) != nil {
			return nil, pluginapi.ErrInvalid
		}
	} else if !os.IsNotExist(err) {
		return nil, pluginapi.ErrInvalid
	}
	syscall.Umask(0077)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, pluginapi.ErrInvalid
	}
	if os.Chmod(path, 0660) != nil {
		listener.Close()
		return nil, pluginapi.ErrInvalid
	}
	return listener, nil
}

func errorsIsConnectionRefused(err error) bool {
	for err != nil {
		if err == syscall.ECONNREFUSED {
			return true
		}
		value, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = value.Unwrap()
	}
	return false
}

func stop() {
	fmt.Fprintln(os.Stderr, "Synthetic payment fixture unavailable; startup or protocol configuration rejected.")
	os.Exit(1)
}
