package main

import (
	"context"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

const releaseChannelRefreshInterval = 15 * time.Second

// bootReleaseChannelDirectory mirrors who chose Preview (CI-CD-PLATFORM-STANDARD
// §4.6.2): stable reads it from kombify Cloud and hands those people over,
// Preview reads the hand-over from stable (pkg/releasechannel). Without a
// directory stable owns every tenant and Preview none, which is the safe
// default for a self-hosted install or a missing service credential.
func bootReleaseChannelDirectory(ctx context.Context, cfg *config.Config, log *logger.Logger) *releasechannel.Directory {
	if cfg == nil || !cfg.DeploymentMode.IsSaaS() {
		return nil
	}
	secret, _, err := servicecall.LoadKeysFromEnv()
	if err != nil {
		log.Warn("release_channel_directory_disabled", "reason", "service_auth_invalid")
		return nil
	}
	source, err := releasechannel.NewSource(secret, nil)
	if err != nil {
		log.Warn("release_channel_directory_disabled", "error", err)
		return nil
	}
	directory := releasechannel.NewDirectory(source)
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := directory.Refresh(readCtx); err != nil {
		log.Warn("release_channel_directory_initial_read_failed", "error", err)
	}
	cancel()
	releasechannel.Install(directory)
	return directory
}

// runReleaseChannelDirectory keeps the directory fresh, logging the first
// failure of each outage only.
func runReleaseChannelDirectory(directory *releasechannel.Directory, log *logger.Logger) func(context.Context) {
	return func(ctx context.Context) {
		failing := false
		directory.Run(ctx, releaseChannelRefreshInterval, func(err error) {
			if err != nil && !failing {
				log.Warn("release_channel_directory_read_failed", "error", err)
			}
			failing = err != nil
		})
	}
}

// registerReleaseChannelRoutes serves stable's hand-over list to Preview.
func registerReleaseChannelRoutes(router *httpx.Router, deps routeDeps) {
	if !config.RunsSharedWork() || deps.startup == nil || deps.startup.cfg == nil || !deps.startup.cfg.DeploymentMode.IsSaaS() {
		return
	}
	secret, next, err := servicecall.LoadKeysFromEnv()
	if err != nil {
		deps.log.Warn("release_channel_handover_disabled", "reason", "service_auth_invalid")
		return
	}
	handler, err := releasechannel.HandOverHandler(secret, next)
	if err != nil {
		deps.log.Warn("release_channel_handover_disabled", "error", err)
		return
	}
	router.GET(releasechannel.HandOverPath, func(e *httpx.Event) error {
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	})
}

// chosenReleaseChannel is the channel subject chose, "" while unknown.
func chosenReleaseChannel(subject string) string {
	preview, known := releasechannel.ChoosesPreview(subject)
	switch {
	case !known:
		return ""
	case preview:
		return "preview"
	default:
		return "stable"
	}
}
