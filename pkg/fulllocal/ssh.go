package fulllocal

import (
	"context"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// DialSSH negotiates SSH over the connection admitted by Dial. The original
// address remains the host-key identity, while the guarded dial pins the peer.
// Caller cancellation bounds every handshake. Full Local also applies the
// configured timeout to the handshake; Standard keeps ssh.Dial's TCP-only
// config.Timeout semantics.
func DialSSH(ctx context.Context, address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	if Active() && config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
	}
	dialer := &net.Dialer{Timeout: config.Timeout}
	network, err := Dial(dialer.DialContext)(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := network.SetDeadline(deadline); err != nil {
			_ = network.Close()
			return nil, err
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = network.Close() })
	connection, channels, requests, err := ssh.NewClientConn(network, address, config)
	stopped := stop()
	if ctx.Err() != nil {
		_ = network.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		_ = network.Close()
		return nil, err
	}
	if !stopped {
		_ = connection.Close()
		return nil, ctx.Err()
	}
	if err := network.SetDeadline(time.Time{}); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return ssh.NewClient(connection, channels, requests), nil
}
