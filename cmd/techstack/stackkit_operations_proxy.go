package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/kombifyio/techstack/pkg/guardexec"
)

const (
	stackKitOperationsSocketEnv = "TECHSTACK_GUARD_OPERATIONS_SOCKET"
	// proxyStatusOK and proxyStatusError prefix the daemon's reply.
	proxyStatusOK    byte = 0
	proxyStatusError byte = 1
)

// stackKitOperationsSocket returns the daemon's operations socket when this
// host runs the privilege-split layout, "" otherwise.
func stackKitOperationsSocket() string {
	socket := envDefault(stackKitOperationsSocketEnv, guardexec.DefaultOperationsSocket)
	if info, err := os.Stat(socket); err == nil && info.Mode()&os.ModeSocket != 0 {
		return socket
	}
	return ""
}

// proxyStackKitOperationsProcess forwards one request to the daemon and writes
// its answer. It never reads the enrollment or the host key.
func proxyStackKitOperationsProcess(ctx context.Context, input io.Reader, output io.Writer, socket string) error {
	payload, err := io.ReadAll(io.LimitReader(input, maxStackKitOperationsPayload+1))
	if err != nil || len(payload) == 0 || len(payload) > maxStackKitOperationsPayload {
		return errors.New("StackKits operations request is empty or exceeds the closed limit")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("the kombify Guard daemon is not reachable: %w", err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("send the StackKits operations request: %w", err)
	}
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
	reply, err := io.ReadAll(io.LimitReader(conn, maxStackKitOperationsPayload+2))
	if err != nil || len(reply) == 0 {
		return errors.New("the kombify Guard daemon sent no answer")
	}
	if reply[0] != proxyStatusOK {
		return errors.New(string(reply[1:]))
	}
	_, err = output.Write(reply[1:])
	return err
}

// serveStackKitOperationsProxy answers operations requests from the root-run
// StackKits executors. The daemon validates each request against its enrolled
// stack and channel and sends it with its own credential, so the credential
// never reaches the privileged side.
func serveStackKitOperationsProxy(ctx context.Context, socket string, enrollment agentEnrollment, client *http.Client, log *slog.Logger) error {
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	allow := operationsPeerPolicy()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handleStackKitOperationsConn(ctx, conn, allow, enrollment, client, log)
	}
}

// operationsPeerPolicy admits root (the executor's StackKits) and the daemon's
// own user.
func operationsPeerPolicy() func(uint32) bool {
	self := uint32(os.Getuid())
	if account, err := user.Current(); err == nil {
		if parsed, parseErr := strconv.ParseUint(account.Uid, 10, 32); parseErr == nil {
			self = uint32(parsed)
		}
	}
	return func(uid uint32) bool { return uid == 0 || uid == self }
}

func handleStackKitOperationsConn(ctx context.Context, conn net.Conn, allow func(uint32) bool, enrollment agentEnrollment, client *http.Client, log *slog.Logger) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	if uid, err := guardexec.PeerUID(conn); err != nil || !allow(uid) {
		log.Warn("guard_operations_peer_refused", "uid", uid)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(conn, maxStackKitOperationsPayload+1))
	if err != nil || len(payload) == 0 || len(payload) > maxStackKitOperationsPayload {
		_, _ = conn.Write(append([]byte{proxyStatusError}, "StackKits operations request is empty or exceeds the closed limit"...))
		return
	}
	result, err := executeStackKitOperationsRequest(ctx, payload, enrollment, client)
	if err != nil {
		_, _ = conn.Write(append([]byte{proxyStatusError}, err.Error()...))
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		_, _ = conn.Write(append([]byte{proxyStatusError}, "the operations result cannot be encoded"...))
		return
	}
	_, _ = conn.Write(append([]byte{proxyStatusOK}, append(encoded, '\n')...))
}
