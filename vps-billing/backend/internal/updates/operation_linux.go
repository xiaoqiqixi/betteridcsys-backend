package updates

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxOperationReceiptBytes = 64 << 10

var errOperationReceipt = errors.New("host operation status unavailable")

// OperationView reports only observed local host-tool state. An audited web
// handoff never creates an operation or changes this read-only receipt.
type OperationView struct {
	State     string     `json:"state"`
	Operation *Operation `json:"operation,omitempty"`
}

// OperationReader is configured by trusted process setup, never by a request.
// It has no execution, service-control or write capability.
type OperationReader struct{ path string }

func NewOperationReader(path string) (*OperationReader, error) {
	if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n")) {
		return nil, errOperationReceipt
	}
	return &OperationReader{path: path}, nil
}

func (r *OperationReader) Snapshot() OperationView {
	if r == nil || r.path == "" {
		return OperationView{State: "unconfigured"}
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return OperationView{State: "unavailable"}
	}
	defer syscall.Close(fd)
	raw, err := readOperationReceiptAt(fd, strings.TrimPrefix(r.path, "/"), 0)
	if errors.Is(err, os.ErrNotExist) {
		return OperationView{State: "absent"}
	}
	if err != nil {
		return OperationView{State: "unavailable"}
	}
	operation, err := parseOperation(raw)
	if err != nil {
		return OperationView{State: "unavailable"}
	}
	return OperationView{State: "available", Operation: &operation}
}

func protectedOperationStat(stat *syscall.Stat_t, directory bool, owner uint32) bool {
	if stat.Uid != owner || stat.Mode&0022 != 0 || stat.Mode&07000 != 0 {
		return false
	}
	if directory {
		return stat.Mode&syscall.S_IFMT == syscall.S_IFDIR
	}
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG && stat.Nlink == 1 && stat.Size > 0 && stat.Size <= maxOperationReceiptBytes
}

// The production entry point starts at / and always requires UID 0. Keeping
// traversal relative to an already-open directory also lets tests exercise the
// same kernel no-follow rules without giving their unprivileged user root.
func readOperationReceiptAt(root int, relative string, owner uint32) ([]byte, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") || strings.ContainsAny(relative, "\x00\r\n") {
		return nil, errOperationReceipt
	}
	fd, err := syscall.Openat(root, ".", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errOperationReceipt
	}
	defer func() {
		if fd >= 0 {
			syscall.Close(fd)
		}
	}()
	var rootStat syscall.Stat_t
	if syscall.Fstat(fd, &rootStat) != nil || !protectedOperationStat(&rootStat, true, owner) {
		return nil, errOperationReceipt
	}
	parts := strings.Split(relative, "/")
	var before syscall.Stat_t
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		directory := i < len(parts)-1
		if directory {
			flags |= syscall.O_DIRECTORY
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		if err != nil {
			if errors.Is(err, syscall.ENOENT) {
				return nil, os.ErrNotExist
			}
			return nil, errOperationReceipt
		}
		syscall.Close(fd)
		fd = next
		var stat syscall.Stat_t
		if syscall.Fstat(fd, &stat) != nil || !protectedOperationStat(&stat, directory, owner) {
			return nil, errOperationReceipt
		}
		if !directory {
			before = stat
		}
	}
	fileFD := fd
	file := os.NewFile(uintptr(fileFD), "host-operation-status")
	fd = -1 // ownership transfers to file; every opened descriptor is CLOEXEC
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxOperationReceiptBytes+1))
	var after syscall.Stat_t
	if err != nil || len(raw) > maxOperationReceiptBytes || int64(len(raw)) != before.Size || syscall.Fstat(fileFD, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid || before.Nlink != after.Nlink || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return nil, errOperationReceipt
	}
	return raw, nil
}
