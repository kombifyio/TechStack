package main

import (
	"log"
	"net/http"

	"github.com/kombifyio/techstack/internal/selfhostbootstrap"
)

func main() {
	config, err := selfhostbootstrap.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	handler, err := selfhostbootstrap.Handler(config)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("TechStack Open Core listening on %s edition=%s mode=%s", config.Address, config.Edition, config.Mode)
	log.Fatal(http.ListenAndServe(config.Address, handler))
}
