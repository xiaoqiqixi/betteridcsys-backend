package modulejobs

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ReadHostFile rejects links, shared-writable ancestors, non-root ownership,
// oversized files and group-readable secrets. It never includes file contents
// or paths in an error. Root-owned sticky temporary ancestors are allowed only
// above the independently checked private chain, not as a writable credential.
func ReadHostFile(path string, private bool, maximum int64) ([]byte, error) {
	return readHostFile(path, private, maximum, 0)
}

func readHostFile(path string, private bool, maximum int64, owner uint32) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") || maximum < 1 {
		return nil, ErrUnavailable
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnavailable
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 && st.Uid != owner || info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return nil, ErrUnavailable
		}
		if parent == "/" {
			break
		}
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "host-configuration")
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > maximum || before.Mode().Perm()&0022 != 0 || private && before.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != owner || st.Nlink != 1 {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, maximum+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || len(raw) == 0 || int64(len(raw)) > maximum || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, ErrUnavailable
	}
	return raw, nil
}
