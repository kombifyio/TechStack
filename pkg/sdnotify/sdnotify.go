// Package sdnotify implements the small part of the systemd notification
// protocol kombify Guard needs: READY=1, STATUS=, STOPPING=1 and a
// progress-gated WATCHDOG=1 keepalive. It has no dependencies and does nothing
// when the process was not started by systemd with NOTIFY_SOCKET.
package sdnotify

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Notify sends one notification datagram to the service manager. It reports
// false without error when no notification socket is configured.
func Notify(state string) (bool, error) {
	return notifyTo(os.Getenv("NOTIFY_SOCKET"), state)
}

func notifyTo(socket, state string) (bool, error) {
	if socket == "" {
		return false, nil
	}
	// A leading "@" names a Linux abstract socket, which is addressed with a
	// leading NUL byte.
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return false, err
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write([]byte(state)); err != nil {
		return false, err
	}
	return true, nil
}

// Ready tells the service manager the service finished starting.
func Ready() { _, _ = Notify("READY=1") }

// Status publishes a free-form status line shown by systemctl status.
func Status(text string) { _, _ = Notify("STATUS=" + strings.ReplaceAll(text, "\n", " ")) }

// Stopping tells the service manager a clean shutdown began.
func Stopping() { _, _ = Notify("STOPPING=1") }

// WatchdogInterval returns the keepalive period systemd expects (a third of
// WatchdogSec), or zero when no watchdog is armed for this process.
func WatchdogInterval() time.Duration {
	return watchdogInterval(os.Getenv("WATCHDOG_USEC"), os.Getenv("WATCHDOG_PID"), os.Getpid())
}

func watchdogInterval(usec, pid string, self int) time.Duration {
	if pid != "" {
		if parsed, err := strconv.Atoi(pid); err != nil || parsed != self {
			return 0
		}
	}
	value, err := strconv.ParseInt(usec, 10, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return time.Duration(value) * time.Microsecond / 3
}

// Watchdog keeps systemd's watchdog satisfied only while the service reports
// progress. A service that hangs stops calling Progress, the keepalive stops,
// and systemd restarts it; a service that is merely disconnected keeps
// progressing and is left alone.
type Watchdog struct {
	last     atomic.Int64
	maxStall time.Duration
	notify   func(string) (bool, error)
	now      func() time.Time
}

// NewWatchdog creates a watchdog that tolerates maxStall without progress.
func NewWatchdog(maxStall time.Duration) *Watchdog {
	watchdog := &Watchdog{maxStall: maxStall, notify: Notify, now: time.Now}
	watchdog.Progress()
	return watchdog
}

// Progress records that the supervised loop is still making progress. It is
// safe to call from any goroutine and on a nil receiver.
func (w *Watchdog) Progress() {
	if w == nil {
		return
	}
	w.last.Store(w.now().UnixNano())
}

func (w *Watchdog) healthy() bool {
	return w.now().Sub(time.Unix(0, w.last.Load())) <= w.maxStall
}

// Run sends WATCHDOG=1 at the interval systemd expects while progress is
// recent. It returns when ctx ends, or immediately when no watchdog is armed.
func (w *Watchdog) Run(ctx context.Context) {
	if w == nil {
		return
	}
	interval := WatchdogInterval()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.healthy() {
				_, _ = w.notify("WATCHDOG=1")
			}
		}
	}
}
