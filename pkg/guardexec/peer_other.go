//go:build !linux

package guardexec

import (
	"errors"
	"net"
)

// PeerUID is unsupported off Linux: the privileged executor is a Linux service.
func PeerUID(net.Conn) (uint32, error) {
	return 0, errors.New("the privileged executor is only available on Linux")
}
