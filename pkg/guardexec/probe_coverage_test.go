package guardexec

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os/exec"
	"testing"
)

type coverageProbeBackend struct {
	fakeBackend
	probe func(context.Context, string) ([]byte, error)
}

func (b *coverageProbeBackend) ProbeDocker(ctx context.Context, kind string) ([]byte, error) {
	return b.probe(ctx, kind)
}

// The privileged wire must retain absence versus partial/failed evidence, so
// a valid prefix can never masquerade as complete container discovery.
func TestPrivilegedProbeCoverageSurvivesTheWire(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		name   string
		probe  func(context.Context, string) ([]byte, error)
		absent bool
		code   string
	}{
		{"absent", (&Host{}).ProbeDocker, true, CodeNotFound},
		{"truncated", func(context.Context, string) ([]byte, error) {
			writer := &limitedWriter{buffer: &bytes.Buffer{}, limit: 8}
			_, _ = writer.Write([]byte("valid\nmore output"))
			return limitedProbeOutput(writer)
		}, false, CodeProbeTruncated},
		{"refused", func(context.Context, string) ([]byte, error) { return nil, errors.New("permission refused") }, false, CodeFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{Backend: &coverageProbeBackend{probe: test.probe}}
			// Exercise the actual request/frame codecs with an owned pipe. Kernel
			// peer authorization belongs to the Linux executor's separate tests.
			client := &Client{Dial: func(context.Context) (net.Conn, error) {
				daemon, executor := net.Pipe()
				go func() {
					defer executor.Close()
					line, err := readLine(bufio.NewReader(executor), MaxRequestBytes)
					if err != nil {
						t.Error(err)
						return
					}
					var request Request
					if err := decodeStrict(line, &request); err != nil {
						t.Error(err)
						return
					}
					writer := &frameWriter{w: executor}
					if denied := server.dispatch(context.Background(), request, writer); denied != nil {
						if err := writer.deny(denied); err != nil {
							t.Error(err)
						}
					}
				}()
				return daemon, nil
			}}
			remote := &Remote{Client: client}
			output, err := remote.ProbeDockerKind(t.Context(), DockerProbeContainers)
			if errors.Is(err, exec.ErrNotFound) != test.absent || !IsCode(err, test.code) || len(output) != 0 {
				t.Fatalf("coverage lost across executor: output=%q err=%v", output, err)
			}
		})
	}
}
