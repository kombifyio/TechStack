package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kombifyio/techstack/internal/substrate"
)

// runSubstrateMode is node-admin configuration and readback. Guest mutations
// remain available only through the admitted lease/Guard execution channel.
func runSubstrateMode(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: techstack substrate profiles|configure|inventory")
	}
	if args[0] == "profiles" {
		return json.NewEncoder(os.Stdout).Encode(substrate.Profiles())
	}
	flags := flag.NewFlagSet("substrate "+args[0], flag.ContinueOnError)
	configPath := flags.String("config", "/var/lib/kombify/substrate.json", "node-local configuration file")
	node := flags.String("node", "", "exact Proxmox node name")
	tokenFile := flags.String("token-file", "", "existing private scoped API token file (0600)")
	certificateFile := flags.String("certificate-file", "/etc/pve/local/pve-ssl.pem", "locally trusted API leaf certificate")
	minID := flags.Int("min-guest-id", 0, "first id in the reserved guest range")
	maxID := flags.Int("max-guest-id", 0, "last id in the reserved guest range")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "configure" {
		encoded, err := os.ReadFile(*certificateFile)
		if err != nil {
			return err
		}
		block, _ := pem.Decode(encoded)
		if block == nil {
			return errors.New("API certificate is not PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(cert.Raw)
		cfg := substrate.Config{Endpoint: "https://127.0.0.1:8006", Node: *node, TokenFile: *tokenFile, CertificateSHA256: hex.EncodeToString(digest[:]), MinGuestID: *minID, MaxGuestID: *maxID}
		client, err := substrate.NewClient(cfg)
		if err != nil {
			return err
		}
		if _, err := client.Inventory(ctx); err != nil {
			return fmt.Errorf("verify scoped substrate token: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(*configPath), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(*configPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err = json.NewEncoder(file).Encode(cfg); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintln(os.Stdout, "Substrate API verified and local configuration written. Restart the enrolled Guard to activate its lease-bound channel.")
		return nil
	}
	if args[0] != "inventory" {
		return errors.New("unknown substrate command")
	}
	cfg, err := substrate.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	client, err := substrate.NewClient(cfg)
	if err != nil {
		return err
	}
	inventory, err := client.Inventory(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(inventory)
}
