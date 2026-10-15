package guardexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// DefaultSocket is where the service manager listens for the executor.
const DefaultSocket = "/run/techstack-guard-executor.sock"

// Client is the daemon side of the boundary.
type Client struct {
	// Socket is the executor's Unix socket path.
	Socket string
	// Dial replaces the default Unix dial (tests).
	Dial func(ctx context.Context) (net.Conn, error)
}

// Error is a refusal or failure reported by the executor.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// IsCode reports whether err is an executor refusal with the given code.
func IsCode(err error, code string) bool {
	var executorErr *Error
	return errors.As(err, &executorErr) && executorErr.Code == code
}

// Call sends one request and feeds every non-terminal frame to handle. It returns
// the payload of the terminal result frame. Cancelling ctx closes the connection,
// which makes the executor stop its work.
func (c *Client) Call(ctx context.Context, op string, params any, handle func(Frame)) (json.RawMessage, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, &Error{Code: CodeUnavailable, Message: fmt.Sprintf("the privileged executor is not reachable: %v", err)}
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	request := Request{Version: ProtocolVersion, Op: op}
	if params != nil {
		if request.Params, err = json.Marshal(params); err != nil {
			return nil, err
		}
	}
	sendErr := writeRecord(conn, request)
	reader := bufio.NewReaderSize(conn, 256<<10)
	if sendErr != nil {
		// A refusing executor may have answered and closed before it read the
		// request; its denial is more useful than a broken pipe.
		if line, readErr := readLine(reader, MaxFrameBytes); readErr == nil {
			var frame Frame
			if json.Unmarshal(line, &frame) == nil && frame.Type == FrameError {
				return nil, &Error{Code: frame.Code, Message: frame.Message}
			}
		}
		return nil, &Error{Code: CodeUnavailable, Message: fmt.Sprintf("the request could not be sent: %v", sendErr)}
	}
	for {
		line, err := readLine(reader, MaxFrameBytes)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &Error{Code: CodeUnavailable, Message: fmt.Sprintf("the executor closed the connection: %v", err)}
		}
		var frame Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			return nil, &Error{Code: CodeFailed, Message: "the executor sent an invalid frame"}
		}
		switch frame.Type {
		case FrameResult:
			return frame.Payload, nil
		case FrameError:
			return nil, &Error{Code: frame.Code, Message: frame.Message}
		default:
			if handle != nil {
				handle(frame)
			}
		}
	}
}

// CallInto is Call with a typed result.
func (c *Client) CallInto(ctx context.Context, op string, params, result any) error {
	payload, err := c.Call(ctx, op, params, nil)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(payload, result)
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	if c.Dial != nil {
		return c.Dial(ctx)
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	return dialer.DialContext(ctx, "unix", c.Socket)
}
