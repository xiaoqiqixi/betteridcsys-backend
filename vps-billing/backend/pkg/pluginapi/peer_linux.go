package pluginapi

import (
	"net"
	"syscall"
)

func PeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, ErrInvalid
	}
	var credentials *syscall.Ucred
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		credentials, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || socketErr != nil || credentials == nil {
		return 0, ErrInvalid
	}
	return credentials.Uid, nil
}
