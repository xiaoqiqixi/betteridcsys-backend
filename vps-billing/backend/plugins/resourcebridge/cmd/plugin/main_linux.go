package main

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginserver"
	"aster/billing/plugins/resourcebridge"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	socket := flag.String("socket", "", "Protected dedicated Unix socket")
	clientUID := flag.Uint("client-uid", 1, "Required root host-dispatch UID")
	controller := flag.String("controller-id", "", "Pinned controller identity")
	targetsJSON := flag.String("targets", "[]", "Approved nonsecret controller destination")
	manifestOnly := flag.Bool("manifest", false, "Print this running binary's manifest without credentials")
	flag.Parse()
	if flag.NArg() != 0 {
		stop()
	}
	self, e := os.Open("/proc/self/exe")
	if e != nil {
		stop()
	}
	hash := sha256.New()
	size, e := io.Copy(hash, io.LimitReader(self, (64<<20)+1))
	self.Close()
	if e != nil || size < 64 || size > 64<<20 {
		stop()
	}
	manifest := pluginapi.Manifest{Schema: 1, ID: resourcebridge.ID, Version: resourcebridge.Version, Protocol: pluginapi.ResourceProtocol, CoreMin: pluginapi.ResourceMinCoreVersion, CoreMaxExclusive: "0.6.0", Capabilities: pluginapi.ResourceCapabilities(), Executable: "plugin", SHA256: hex.EncodeToString(hash.Sum(nil))}
	if *manifestOnly {
		printManifest(manifest)
		return
	}
	if os.Geteuid() == 0 || *clientUID != 0 || !pluginapi.ValidControllerID(*controller) {
		stop()
	}
	var targets []pluginapi.Target
	if pluginapi.DecodeStrict([]byte(*targetsJSON), pluginapi.MaxPolicyBytes, &targets) != nil {
		stop()
	}
	directory := os.Getenv("CREDENTIALS_DIRECTORY")
	raw, e := resourcebridge.ReadCredential(directory, "resource-policy")
	if e != nil {
		stop()
	}
	cfg, e := resourcebridge.DecodeConfig(raw, *controller, targets)
	clear(raw)
	if e != nil {
		stop()
	}
	token, e := resourcebridge.ReadCredential(directory, "resource-token")
	if e != nil {
		stop()
	}
	var ca []byte
	if cfg.CustomCA {
		ca, e = resourcebridge.ReadCredential(directory, "resource-ca")
		if e != nil {
			clear(token)
			stop()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	processor, e := resourcebridge.New(ctx, cfg, token, ca)
	cancel()
	clear(token)
	clear(ca)
	if e != nil {
		stop()
	}
	listener, e := listen(*socket)
	if e != nil {
		stop()
	}
	defer listener.Close()
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	if (pluginserver.ResourceServer{Manifest: manifest, ClientUID: 0, ControllerID: *controller, Processor: processor}).Serve(signalCtx, listener) != nil {
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
	// Never unlink an existing path on startup. RuntimeDirectoryPreserve=no
	// makes systemd cleanup the owned directory after the worker has stopped.
	syscall.Umask(0077)
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		return nil, pluginapi.ErrInvalid
	}
	if os.Chmod(path, 0600) != nil {
		listener.Close()
		return nil, pluginapi.ErrInvalid
	}
	return listener, nil
}
func stop() {
	fmt.Fprintln(os.Stderr, "Resource worker unavailable; approved installation or credentials rejected.")
	os.Exit(1)
}

func printManifest(m pluginapi.Manifest) {
	if json.NewEncoder(os.Stdout).Encode(m) != nil {
		stop()
	}
}
