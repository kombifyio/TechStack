package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/platformprojection"
)

func runPlatformProjectionServer(ctx context.Context) error {
	addr := strings.TrimSpace(os.Getenv("TECHSTACK_EXECUTION_LISTEN"))
	if addr == "" {
		addr = "127.0.0.1:8097"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("platform projection health requires a loopback listener")
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	projection, err := platformprojection.OpenFromEnv(startup)
	cancel()
	if err != nil {
		return err
	}
	defer projection.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		status := "inert"
		if projection.Enabled {
			status = "pending"
		}
		if projection.Ready() {
			status = projectionReady
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if !projection.Ready() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(platformProjectionHealth{Status: status, Version: version, Revision: buildRevision, SourceRevision: buildRevision, Profile: "platform-projection", ReleaseChannel: os.Getenv("TECHSTACK_RELEASE_CHANNEL"), Enabled: projection.Enabled, Ready: projection.Ready()})
	})
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerCtx, cancelWorker := context.WithCancel(signalCtx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); projection.Run(workerCtx) }()
	defer func() { cancelWorker(); <-workerDone }()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signalCtx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

const projectionReady = "ready"

type platformProjectionHealth struct {
	Status         string `json:"status"`
	Version        string `json:"version"`
	Revision       string `json:"revision"`
	SourceRevision string `json:"source_revision"`
	Profile        string `json:"profile"`
	ReleaseChannel string `json:"release_channel"`
	Enabled        bool   `json:"enabled"`
	Ready          bool   `json:"ready"`
}
