package main

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/ril/signals"
)

const rilSignalGatewayURLEnv = "TECHSTACK_RIL_SIGNAL_GATEWAY_URL"

func bootRILSignalRuntime(v2 *v2Boot, revision string, log *logger.Logger) (*signals.PostgresOutbox, *signals.Worker) {
	if v2 == nil || v2.db == nil || v2.db.DB == nil {
		return nil, nil
	}
	outbox := signals.NewPostgresOutbox(v2.db.DB)
	secret := strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET"))
	if secret == "" {
		log.Warn("ril_signal_publisher_disabled", "reason", "SERVICE_AUTH_SECRET missing")
		return outbox, nil
	}
	gatewayURL := strings.TrimSpace(os.Getenv(rilSignalGatewayURLEnv))
	if gatewayURL == "" {
		gatewayURL = signals.DefaultGatewaySignalURL
	}
	publisher, err := signals.NewGatewayPublisher(
		gatewayURL,
		secret,
		&http.Client{Timeout: 15 * time.Second},
	)
	if err != nil {
		log.Warn("ril_signal_publisher_disabled", "reason", err.Error())
		return outbox, nil
	}
	ownerSuffix := strings.TrimSpace(revision)
	if len(ownerSuffix) > 12 {
		ownerSuffix = ownerSuffix[:12]
	}
	if ownerSuffix == "" || ownerSuffix == "dev" {
		ownerSuffix = "local"
	}
	worker := &signals.Worker{
		Outbox: outbox, Publisher: publisher,
		Owner: "ril-publisher/" + ownerSuffix, Lease: 30 * time.Second,
		Poll: 2 * time.Second,
		OnError: func(err error) {
			log.Warn("ril_signal_publish_failed", "error", err)
		},
	}
	log.Info("ril_signal_publisher_ready", "gateway", gatewayURL)
	return outbox, worker
}
