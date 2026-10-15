package main

import (
	"errors"

	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/originca"
)

// composeOriginCertificates wires the managed kombify.me origin certificate
// service (ADR-0047). It is nil only without a database for custody. A missing
// CLOUDFLARE_KOMBIFY_ME_ORIGIN_CA_TOKEN does not remove the service: it makes
// every issuance fail closed with a typed error naming the variable, while a
// destroy of an install without a certificate still succeeds.
func composeOriginCertificates(boot *v2Boot, log *logger.Logger) *originca.Service {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	store, err := originca.NewPostgresStore(boot.db.DB)
	if err != nil {
		return nil
	}
	config, configErr := originca.FromEnv()
	if configErr != nil {
		var missing *originca.TokenMissingError
		if log != nil && errors.As(configErr, &missing) {
			log.Warn("origin_certificate_token_missing", "env", originca.TokenEnv)
		}
		return originca.NewService(nil, configErr, store)
	}
	client, err := originca.NewClient(config)
	if err != nil {
		return originca.NewService(nil, err, store)
	}
	return originca.NewService(client, nil, store)
}
