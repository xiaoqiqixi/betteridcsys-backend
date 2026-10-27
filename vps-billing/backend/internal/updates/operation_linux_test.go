package updates

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func operationTestRoot(t *testing.T) (int, string) {
	t.Helper()
	dir := t.TempDir()
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	return fd, dir
}

func TestOperationReceiptProtectedTraversalAndReadOnlyGroup(t *testing.T) {
	fd, dir := operationTestRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "operation"), 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "operation", "STATUS.json")
	value := []byte(`{"synthetic":"receipt"}`)
	if err := os.WriteFile(path, value, 0640); err != nil {
		t.Fatal(err)
	}
	got, err := readOperationReceiptAt(fd, "operation/STATUS.json", uint32(os.Geteuid()))
	if err != nil || !bytes.Equal(got, value) {
		t.Fatal("protected read-only group receipt was rejected", err)
	}
	if _, err := readOperationReceiptAt(fd, "missing/STATUS.json", uint32(os.Geteuid())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("absent receipt was not distinguished", err)
	}
	// Reads never change the host tool's output.
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, value) {
		t.Fatal("read changed receipt", err)
	}
}

func TestOperationReceiptRejectsUnsafeFilesystemObjects(t *testing.T) {
	for _, defect := range []string{"file_group_write", "file_other_write", "ancestor_group_write", "ancestor_other_write", "ancestor_symlink", "file_symlink", "hardlink", "fifo", "empty", "oversized", "wrong_owner", "root_group_write"} {
		t.Run(defect, func(t *testing.T) {
			fd, dir := operationTestRoot(t)
			ancestor := filepath.Join(dir, "operation")
			if err := os.Mkdir(ancestor, 0750); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(ancestor, "STATUS.json")
			if err := os.WriteFile(path, []byte(`{}`), 0640); err != nil {
				t.Fatal(err)
			}
			owner := uint32(os.Geteuid())
			var err error
			switch defect {
			case "file_group_write":
				err = os.Chmod(path, 0660)
			case "file_other_write":
				err = os.Chmod(path, 0642)
			case "ancestor_group_write":
				err = os.Chmod(ancestor, 0770)
			case "ancestor_other_write":
				err = os.Chmod(ancestor, 0777|os.ModeSticky)
			case "ancestor_symlink":
				err = os.Rename(ancestor, ancestor+"-real")
				if err == nil {
					err = os.Symlink(ancestor+"-real", ancestor)
				}
			case "file_symlink":
				err = os.Rename(path, path+"-real")
				if err == nil {
					err = os.Symlink(path+"-real", path)
				}
			case "hardlink":
				err = os.Link(path, path+"-alias")
			case "fifo":
				err = os.Remove(path)
				if err == nil {
					err = syscall.Mkfifo(path, 0640)
				}
			case "empty":
				err = os.WriteFile(path, nil, 0640)
			case "oversized":
				err = os.WriteFile(path, bytes.Repeat([]byte{'x'}, maxOperationReceiptBytes+1), 0640)
			case "wrong_owner":
				owner++
			case "root_group_write":
				err = os.Chmod(dir, 0770)
			}
			if err != nil {
				t.Fatal("fixture", err)
			}
			if raw, err := readOperationReceiptAt(fd, "operation/STATUS.json", owner); !errors.Is(err, errOperationReceipt) || raw != nil {
				t.Fatal("unsafe receipt was accepted or exposed", defect, err)
			}
		})
	}
}

func TestOperationReceiptPathConfigurationAndUnavailableRedaction(t *testing.T) {
	for _, path := range []string{"relative/STATUS.json", "/", "/var/lib/../secret", "/var/lib//secret", "/var/lib/secret/", "/var/lib/secret\n", "/var/lib/secret\x00"} {
		reader, err := NewOperationReader(path)
		if err == nil || reader != nil || strings.Contains(err.Error(), path) {
			t.Fatal("unsafe configured path accepted or exposed")
		}
	}
	for _, reader := range []*OperationReader{nil, {}} {
		if got := reader.Snapshot(); got.State != "unconfigured" || got.Operation != nil {
			t.Fatal("unconfigured receipt invented operation state")
		}
	}
	fd, _ := operationTestRoot(t)
	for _, relative := range []string{"", ".", "..", "../secret", "/secret", "nested/../../secret", "nested//secret", "nested/secret\n"} {
		if raw, err := readOperationReceiptAt(fd, relative, uint32(os.Geteuid())); !errors.Is(err, errOperationReceipt) || raw != nil {
			t.Fatal("relative traversal accepted", relative)
		}
	}
}

func TestOperationReceiptProductionStatPolicyAlwaysRequiresRoot(t *testing.T) {
	good := syscall.Stat_t{Uid: 0, Mode: syscall.S_IFREG | 0640, Nlink: 1, Size: maxOperationReceiptBytes}
	if !protectedOperationStat(&good, false, 0) {
		t.Fatal("root-owned read-only application group rejected")
	}
	for _, defect := range []string{"owner", "group_write", "other_write", "setuid", "setgid", "sticky", "directory", "symlink", "hardlink", "size", "empty"} {
		stat := good
		switch defect {
		case "owner":
			stat.Uid = 1000
		case "group_write":
			stat.Mode |= 0020
		case "other_write":
			stat.Mode |= 0002
		case "setuid":
			stat.Mode |= syscall.S_ISUID
		case "setgid":
			stat.Mode |= syscall.S_ISGID
		case "sticky":
			stat.Mode |= syscall.S_ISVTX
		case "directory":
			stat.Mode = syscall.S_IFDIR | 0750
		case "symlink":
			stat.Mode = syscall.S_IFLNK | 0640
		case "hardlink":
			stat.Nlink = 2
		case "size":
			stat.Size++
		case "empty":
			stat.Size = 0
		}
		if protectedOperationStat(&stat, false, 0) {
			t.Fatal("production policy accepted", defect)
		}
	}
}
