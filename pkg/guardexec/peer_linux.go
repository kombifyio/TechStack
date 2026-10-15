//go:build linux

package guardexec

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// PeerUID returns the uid the kernel recorded for the connecting process.
func PeerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("the executor only serves Unix sockets")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	return credentials.Uid, nil
}
