package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginserver"
	"aster/billing/plugins/smtp"
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

// Set by the independent SMTP package build, never by a runtime request.
var version = "1.1.0"

func main() {
	showVersion := flag.Bool("version", false, "Print SMTP plugin version")
	socket := flag.String("socket", "", "Private Unix socket")
	clientUID := flag.Uint("client-uid", 0, "Allowed core UID")
	targetsJSON := flag.String("targets", "", "Approved nonsecret SMTP destinations")
	flag.Parse()
	if flag.NArg() != 0 || !pluginapi.ValidVersion(version) {
		stop()
	}
	if *showVersion {
		fmt.Println("Aster SMTP plugin " + version)
		return
	}
	if os.Geteuid() == 0 || *clientUID == 0 || *clientUID > uint(^uint32(0)) || *clientUID == uint(os.Geteuid()) {
		stop()
	}
	var targets []pluginapi.Target
	if pluginapi.DecodeStrict([]byte(*targetsJSON), pluginapi.MaxPolicyBytes, &targets) != nil || len(targets) == 0 || len(targets) > 8 {
		stop()
	}
	for _, target := range targets {
		if target.Host == "" || len(target.Host) > 253 || strings.ContainsAny(target.Host, "/\\\x00\r\n\t ") || target.Port < 1 || target.Port > 65535 {
			stop()
		}
	}
	// This fixed kernel path pins the running executable, including an inode
	// retained while an administrator installs another immutable version.
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
	manifest := pluginapi.Manifest{Schema: 1, ID: "email.smtp", Version: version, Protocol: pluginapi.SMTPProtocol, CoreMin: "0.4.0", CoreMaxExclusive: "0.6.0", Capabilities: []string{pluginapi.SMTPCapability}, Executable: "plugin", SHA256: hex.EncodeToString(hash.Sum(nil))}
	listener, err := listen(*socket)
	if err != nil {
		stop()
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	server := pluginserver.Server{Manifest: manifest, ClientUID: uint32(*clientUID), Targets: targets, Sender: smtp.NewClient()}
	if server.Serve(ctx, listener) != nil {
		stop()
	}
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
		// Only a provably stale socket may be removed, not a live, inaccessible,
		// or replaced endpoint. systemd permits one service instance at a time.
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
	fmt.Fprintln(os.Stderr, "SMTP plugin unavailable; startup or protocol configuration rejected.")
	os.Exit(1)
}
