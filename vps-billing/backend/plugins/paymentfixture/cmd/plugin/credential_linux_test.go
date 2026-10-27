package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func workerACL(uid uint32) []byte {
	const undefinedID = ^uint32(0)
	data := make([]byte, 4+5*8)
	binary.LittleEndian.PutUint32(data, 2)
	for n, entry := range []struct {
		tag, permission uint16
		id              uint32
	}{
		{1, 4, undefinedID}, {2, 4, uid}, {4, 0, undefinedID}, {16, 4, undefinedID}, {32, 0, undefinedID},
	} {
		offset := 4 + n*8
		binary.LittleEndian.PutUint16(data[offset:], entry.tag)
		binary.LittleEndian.PutUint16(data[offset+2:], entry.permission)
		binary.LittleEndian.PutUint32(data[offset+4:], entry.id)
	}
	return data
}

func TestFixtureKeyAcceptsOnlyWorkerReadACL(t *testing.T) {
	const workerUID uint32 = 12345
	valid := workerACL(workerUID)
	if !validFixtureKeyACL(valid, workerUID) {
		t.Fatal("systemd worker-only read ACL rejected")
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"wrong version":     func(raw []byte) []byte { binary.LittleEndian.PutUint32(raw, 1); return raw },
		"short":             func(raw []byte) []byte { return raw[:len(raw)-1] },
		"extra":             func(raw []byte) []byte { return append(raw, make([]byte, 8)...) },
		"other uid":         func(raw []byte) []byte { binary.LittleEndian.PutUint32(raw[16:], workerUID+1); return raw },
		"named group":       func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[12:], 8); return raw },
		"duplicate owner":   func(raw []byte) []byte { copy(raw[12:20], raw[4:12]); return raw },
		"missing mask":      func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[28:], 64); return raw },
		"owner write":       func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[6:], 6); return raw },
		"owner execute":     func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[6:], 5); return raw },
		"worker write":      func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[14:], 6); return raw },
		"worker execute":    func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[14:], 5); return raw },
		"owning group read": func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[22:], 4); return raw },
		"other read":        func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[38:], 4); return raw },
		"mask write":        func(raw []byte) []byte { binary.LittleEndian.PutUint16(raw[30:], 6); return raw },
		"object id":         func(raw []byte) []byte { binary.LittleEndian.PutUint32(raw[8:], workerUID); return raw },
	} {
		t.Run(name, func(t *testing.T) {
			if validFixtureKeyACL(mutate(bytes.Clone(valid)), workerUID) {
				t.Fatal("unsafe credential ACL accepted")
			}
		})
	}
	if validFixtureKeyACL(valid, 0) {
		t.Fatal("root worker accepted")
	}
}

func TestFixtureKeyACLUsesOpenedInodeAndRootOwnerOnly(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "payment-fixture-key")
	key := bytes.Repeat([]byte{0x81}, 32)
	if e := os.WriteFile(path, key, 0600); e != nil {
		t.Fatal(e)
	}
	file, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	workerUID := uint32(os.Geteuid())
	if workerUID == 0 {
		workerUID = 1
	}
	// Use a mapped UID: isolated user namespaces may map only the current UID.
	if e = syscall.Setxattr(path, "system.posix_acl_access", workerACL(workerUID), 0); e != nil {
		if errors.Is(e, syscall.ENOTSUP) || errors.Is(e, syscall.EOPNOTSUPP) {
			t.Skip("filesystem does not support POSIX access ACLs")
		}
		t.Fatal(e)
	}
	var info syscall.Stat_t
	if e = syscall.Fstat(int(file.Fd()), &info); e != nil {
		t.Fatal(e)
	}
	if info.Mode&0777 != 0440 {
		t.Fatal("worker read ACL did not expose group-class mask", info.Mode&0777)
	}
	if !fixtureKeyWorkerACL(int(file.Fd()), workerUID) {
		t.Fatal("kernel worker-only ACL rejected")
	}
	// Unprivileged tests cannot chown a file to root. Exercise the real inode
	// ACL reader and the root-only metadata branch separately; native systemd
	// acceptance covers the actual root-owned LoadCredential file.
	rootInfo := info
	rootInfo.Uid = 0
	if !fixtureKeyAccess(int(file.Fd()), rootInfo, workerUID) {
		t.Fatal("root-owned systemd ACL form rejected")
	}
	if os.Geteuid() != 0 && fixtureKeyAccess(int(file.Fd()), info, workerUID) {
		t.Fatal("worker-owned group-class permissions accepted")
	}
	if fixtureKeyWorkerACL(int(file.Fd()), workerUID+1) {
		t.Fatal("ACL granted another worker")
	}
	if e = os.Rename(path, path+".pinned"); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, key, 0440); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0440); e != nil {
		t.Fatal(e)
	}
	if !fixtureKeyWorkerACL(int(file.Fd()), workerUID) {
		t.Fatal("ACL read followed a replaced input pathname")
	}
	if _, e = fixtureKey(directory); e == nil {
		t.Fatal("ordinary group-readable replacement accepted")
	}
}
