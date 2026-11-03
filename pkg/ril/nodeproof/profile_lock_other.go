//go:build !linux

package nodeproof

import (
	"context"
	"errors"
	"net"
)

func WithHostedProfileDial(context.Context, string, string, func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	return nil, errors.New("RIL applied profile locking requires Linux")
}

func AcquireHostedProfile(context.Context) (func(), error) {
	return nil, errors.New("RIL applied profile locking requires Linux")
}
