package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestExtendWriteDeadlineKeepsSlowResponsesAlive proves the effect over a real
// socket: a handler that legitimately runs past the server WriteTimeout still
// delivers its response when it extends the deadline, while the same handler
// without the extension is cut off.
func TestExtendWriteDeadlineKeepsSlowResponsesAlive(t *testing.T) {
	const (
		writeTimeout = 150 * time.Millisecond
		handlerSleep = 500 * time.Millisecond
	)

	newSlowServer := func(extend bool) *httptest.Server {
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if extend {
				if err := ExtendWriteDeadline(w, 2*time.Second); err != nil {
					t.Errorf("ExtendWriteDeadline: %v", err)
				}
			}
			time.Sleep(handlerSleep)
			_, _ = io.WriteString(w, "ok")
		})
		server := httptest.NewUnstartedServer(handler)
		server.Config.WriteTimeout = writeTimeout
		server.Start()
		return server
	}

	t.Run("without extension the slow response is cut off", func(t *testing.T) {
		server := newSlowServer(false)
		defer server.Close()
		body, err := readBody(server.URL)
		if err == nil && body == "ok" {
			t.Fatal("slow response without extension unexpectedly completed")
		}
	})

	t.Run("with extension the slow response completes", func(t *testing.T) {
		server := newSlowServer(true)
		defer server.Close()
		body, err := readBody(server.URL)
		if err != nil {
			t.Fatalf("extended slow response error = %v", err)
		}
		if body != "ok" {
			t.Fatalf("extended slow response body = %q, want %q", body, "ok")
		}
	})
}

func readBody(url string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
