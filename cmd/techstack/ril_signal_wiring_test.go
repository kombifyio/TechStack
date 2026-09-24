package main

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/ril/signals"
)

func TestSelfHostedRILSignalsRequireExplicitGateway(t *testing.T) {
	if got := resolveRILSignalGatewayURL(config.EditionSelfHostOSS, ""); got != "" {
		t.Fatal("self-hosted signals selected a hosted gateway without operator configuration")
	}
	if got := resolveRILSignalGatewayURL(config.EditionSelfHostOSS, "https://local.example/signals"); got != "https://local.example/signals" {
		t.Fatal("self-hosted signals ignored the operator gateway")
	}
	if got := resolveRILSignalGatewayURL(config.EditionSaaSStandalone, ""); got != signals.DefaultGatewaySignalURL {
		t.Fatal("hosted signals lost their default gateway")
	}
}
