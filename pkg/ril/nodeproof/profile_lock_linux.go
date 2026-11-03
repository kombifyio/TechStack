//go:build linux

package nodeproof

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
)

// WithHostedProfileDial holds the shared activation lock for the connection's
// whole lifetime. Apply holds the exclusive lock while changing the profile.
func AcquireHostedProfile(ctx context.Context) (func(), error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("RIL profile requires root")
	}
	parent, err := openTrustedDirectory("/run")
	if err != nil {
		return nil, err
	}
	root, err := privatefs.OpenChild(parent, "techstack-ril")
	parent.Close()
	if err != nil {
		return nil, err
	}
	file, err := privatefs.OpenFile(root, "activation.lock", os.O_CREATE|os.O_RDWR, 0o600)
	root.Close()
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	release := func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }
	if err = admitHostedProfile("/var/lib/techstack-guard-executor"); err != nil {
		release()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}

// WithHostedProfileDial holds admission until the connection closes.
func WithHostedProfileDial(ctx context.Context, network, address string, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	release, err := AcquireHostedProfile(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := dial(ctx, network, address)
	if err != nil {
		release()
		return nil, err
	}
	return &profileConnection{Conn: conn, release: release}, nil
}

type profileConnection struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *profileConnection) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
