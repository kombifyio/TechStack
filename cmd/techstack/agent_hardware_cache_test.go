package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/httpguard"
)

// The probe runs a shell, so it must never sit inside the heartbeat: the first
// call reports nothing and starts it, a later call carries the result, and a
// failed refresh keeps the last good reading instead of dropping a fact the
// machine still has.
func TestLocalHardwareCacheProbesInTheBackgroundAndKeepsTheLastGoodReading(t *testing.T) {
	results := make(chan error, 2)
	released := make(chan struct{})
	reading := &httpguard.HostHardware{AMD64Level: 3}
	cache := &localHardwareCache{observe: func(context.Context) (*httpguard.HostHardware, error) {
		err := <-results
		defer func() { released <- struct{}{} }()
		if err != nil {
			return nil, err
		}
		return reading, nil
	}}

	if got := cache.current(t.Context()); got != nil {
		t.Fatalf("the first call returned %+v before any probe finished", got)
	}
	results <- nil
	<-released
	waitForIdle(t, cache)
	if got := cache.current(t.Context()); got != reading {
		t.Fatalf("the finished probe was not reported: %+v", got)
	}

	// Due again, and the refresh fails: the last good reading stays.
	cache.mu.Lock()
	cache.started = time.Now().Add(-2 * localHardwareRefresh)
	cache.mu.Unlock()
	if got := cache.current(t.Context()); got != reading {
		t.Fatalf("a refresh in flight dropped the reading: %+v", got)
	}
	results <- errors.New("sh is not available")
	<-released
	waitForIdle(t, cache)
	if got := cache.current(t.Context()); got != reading {
		t.Fatalf("a failed refresh dropped the last good reading: %+v", got)
	}

	// After a day without a good reading nothing is reported rather than an old answer.
	cache.mu.Lock()
	cache.good = time.Now().Add(-2 * localHardwareKeep)
	cache.mu.Unlock()
	if got := cache.current(t.Context()); got != nil {
		t.Fatalf("a reading older than %s was still reported: %+v", localHardwareKeep, got)
	}
}

func waitForIdle(t *testing.T, cache *localHardwareCache) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		running := cache.running
		cache.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the probe goroutine did not finish")
}
