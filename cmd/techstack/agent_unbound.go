package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/sdnotify"
)

// waitForInstanceEnrollment does not create a host key or perform network work.
// The instance installer owns key creation and confirmation; the enrollment
// response can appear before that confirmation has finished.
func waitForInstanceEnrollment(ctx context.Context, cfg *agentModeConfig, log *slog.Logger) error {
	announced := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		present := false
		for _, path := range []string{cfg.enrollmentFile, filepath.Join(cfg.stateDir, hostidentity.EnrollmentFileName)} {
			_, err := os.Lstat(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			present = present || err == nil
		}
		if present {
			if _, err := resolveHTTPAgentEnrollment(cfg); err != nil {
				return err
			}
			manager, openErr := hostidentity.Open(cfg.stateDir)
			if openErr != nil {
				return openErr
			}
			if manager.Record().State != hostidentity.StateRedeeming {
				return nil
			}
		}
		if !announced {
			log.Info("guard_waiting_for_instance_enrollment")
			sdnotify.Status("unbound; waiting for instance enrollment")
			announced = true
		}
		if cfg.progress != nil {
			cfg.progress()
		}
		if !waitWithProgress(ctx, time.Second, cfg.progress) {
			return ctx.Err()
		}
	}
}
