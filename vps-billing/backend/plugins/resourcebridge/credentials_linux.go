package resourcebridge

import (
	"aster/billing/pkg/pluginapi"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ReadCredential accepts only three fixed names in the systemd credential
// mount. No request ever supplies a name/path. Ownership is root or this worker,
// every parent is protected, and secrets may not be group/world readable.
func ReadCredential(directory, name string) ([]byte, error) {
	if !strings.HasPrefix(directory, "/run/credentials/") {
		return nil, pluginapi.ErrInvalid
	}
	return readCredential(directory, name, uint32(os.Geteuid()))
}
func readCredential(directory, name string, uid uint32) ([]byte, error) {
	maximum := int64(64 << 10)
	switch name {
	case "resource-token":
		maximum = 4096
	case "resource-policy", "resource-ca":
	default:
		return nil, pluginapi.ErrInvalid
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || len(directory) > 1024 || strings.ContainsAny(directory, "\x00\r\n") {
		return nil, pluginapi.ErrInvalid
	}
	path := filepath.Join(directory, name)
	fd, e := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, pluginapi.ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, e := syscall.Openat(fd, part, flags, 0)
		_ = syscall.Close(fd)
		if e != nil {
			return nil, pluginapi.ErrInvalid
		}
		fd = next
		var info syscall.Stat_t
		invalid := syscall.Fstat(fd, &info) != nil || info.Uid != 0 && info.Uid != uid || info.Mode&0022 != 0 || info.Mode&(syscall.S_ISUID|syscall.S_ISGID) != 0
		if i == len(parts)-1 {
			invalid = invalid || info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 || info.Mode&0077 != 0 || info.Size < 1 || info.Size > maximum
		} else {
			invalid = invalid || info.Mode&syscall.S_IFMT != syscall.S_IFDIR
		}
		if invalid {
			_ = syscall.Close(fd)
			return nil, pluginapi.ErrInvalid
		}
	}
	f := os.NewFile(uintptr(fd), "systemd-resource-credential")
	defer f.Close()
	var before, after syscall.Stat_t
	if syscall.Fstat(fd, &before) != nil {
		return nil, pluginapi.ErrInvalid
	}
	raw, e := io.ReadAll(io.LimitReader(f, maximum+1))
	if e != nil || int64(len(raw)) != before.Size || syscall.Fstat(fd, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mode != after.Mode || before.Uid != after.Uid || before.Nlink != after.Nlink || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		clear(raw)
		return nil, pluginapi.ErrInvalid
	}
	return raw, nil
}
