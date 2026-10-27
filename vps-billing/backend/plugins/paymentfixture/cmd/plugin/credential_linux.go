package main

import (
	"encoding/binary"
	"strconv"
	"syscall"
)

// systemd normally keeps credentials root-owned and grants the worker a named
// read ACL. Its ACL mask appears as group-read in stat(2), even though the owning
// group has no permissions. Accept precisely that form, not ordinary 0440 files.
func fixtureKeyAccess(fd int, info syscall.Stat_t, workerUID uint32) bool {
	if info.Mode&0077 == 0 {
		return true
	}
	if info.Uid != 0 || info.Mode&07777 != 0440 {
		return false
	}
	return fixtureKeyWorkerACL(fd, workerUID)
}

func fixtureKeyWorkerACL(fd int, workerUID uint32) bool {
	const aclBytes = 4 + 5*8
	raw := make([]byte, aclBytes)
	// The descriptor remains open until fixtureKey returns. This fixed procfs
	// path resolves that pinned inode, never a replacement at the input pathname.
	path := "/proc/self/fd/" + strconv.Itoa(fd)
	n, err := syscall.Getxattr(path, "system.posix_acl_access", raw)
	return err == nil && n == len(raw) && validFixtureKeyACL(raw, workerUID)
}

func validFixtureKeyACL(raw []byte, workerUID uint32) bool {
	const (
		userObject  = 0x01
		user        = 0x02
		groupObject = 0x04
		mask        = 0x10
		other       = 0x20
		undefinedID = ^uint32(0)
	)
	if len(raw) != 4+5*8 || binary.LittleEndian.Uint32(raw[:4]) != 2 || workerUID == 0 {
		return false
	}
	seen := map[uint16]bool{}
	for offset := 4; offset < len(raw); offset += 8 {
		tag := binary.LittleEndian.Uint16(raw[offset:])
		permission := binary.LittleEndian.Uint16(raw[offset+2:])
		id := binary.LittleEndian.Uint32(raw[offset+4:])
		if seen[tag] {
			return false
		}
		seen[tag] = true
		switch tag {
		case userObject, mask:
			if permission != 4 || id != undefinedID {
				return false
			}
		case user:
			if permission != 4 || id != workerUID {
				return false
			}
		case groupObject, other:
			if permission != 0 || id != undefinedID {
				return false
			}
		default:
			return false
		}
	}
	return len(seen) == 5
}
