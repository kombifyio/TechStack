package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

const walletUsage = "usage: techstack wallet recipient-key generate [--generation N] [--private-out FILE]\n" +
	"       techstack wallet recipient-key verify"

// runWallet dispatches `techstack wallet ...`.
func runWallet(args []string) error {
	return runWalletWith(args, os.Stdout, os.Stderr, os.Getenv)
}

func runWalletWith(args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	if len(args) < 2 || args[0] != "recipient-key" {
		return errors.New(walletUsage)
	}
	switch args[1] {
	case "generate":
		return runRecipientKeyGenerate(args[2:], stdout, stderr)
	case "verify":
		if len(args) != 2 {
			return errors.New(walletUsage)
		}
		return runRecipientKeyVerify(stdout, getenv)
	default:
		return errors.New(walletUsage)
	}
}

// runRecipientKeyGenerate creates the recipient key pair Techstack opens
// Wallet deliveries with. The private JWK goes to --private-out (0600, never
// overwritten) or, without it, to stdout; the public half and its thumbprint
// are printed for the Wallet's WALLET_SERVICE_RECIPIENTS enrolment.
func runRecipientKeyGenerate(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("wallet recipient-key generate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	generation := flags.String("generation", "1", "recipient key generation (decimal)")
	privateOut := flags.String("private-out", "", "write the private key to this new 0600 file instead of stdout")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, walletUsage)
	}
	if flags.NArg() != 0 {
		return errors.New(walletUsage)
	}
	if n, err := strconv.ParseUint(*generation, 10, 32); err != nil || n == 0 {
		return errors.New("--generation must be a positive decimal number")
	}
	private, public, err := walletservice.GenerateRecipientKey()
	if err != nil {
		return fmt.Errorf("generate recipient key: %w", err)
	}
	enrolment, err := json.Marshal(map[string]any{
		kombifywallet.ServiceID: map[string]any{"generation": *generation, "jwk": public},
	})
	if err != nil {
		return err
	}
	if *privateOut != "" {
		file, err := os.OpenFile(*privateOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create private key file: %w", err)
		}
		_, werr := file.WriteString(private + "\n")
		if cerr := file.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("write private key file: %w", werr)
		}
		fmt.Fprintf(stdout, "%s=<contents of %s>\n", kombifywallet.EnvRecipientKey, *privateOut)
		fmt.Fprintln(stderr, "Move the private key file into company secret custody, then delete it.")
	} else {
		fmt.Fprintf(stdout, "%s=%s\n", kombifywallet.EnvRecipientKey, private)
		fmt.Fprintln(stderr, "The private key was printed to stdout: store it in company secret custody and clear the terminal transcript.")
	}
	fmt.Fprintf(stdout, "%s=%s\n", kombifywallet.EnvRecipientGeneration, *generation)
	fmt.Fprintf(stdout, "public_thumbprint=%s\n", public.Thumbprint())
	fmt.Fprintf(stdout, "WALLET_SERVICE_RECIPIENTS=%s\n", enrolment)
	return nil
}

// runRecipientKeyVerify validates the configured recipient key and generation
// exactly as the hosted boot does and prints only public values.
func runRecipientKeyVerify(stdout io.Writer, getenv func(string) string) error {
	generation := strings.TrimSpace(getenv(kombifywallet.EnvRecipientGeneration))
	// The origin and service secret are placeholders: only the key is checked.
	client := walletservice.New(walletservice.Config{
		Origin:              kombifywallet.DefaultServiceURL,
		ServiceID:           kombifywallet.ServiceID,
		ServiceSecret:       "recipient-key-verify",
		RecipientKey:        getenv(kombifywallet.EnvRecipientKey),
		RecipientGeneration: generation,
	})
	if !client.Enabled() {
		return fmt.Errorf("recipient key is not usable: %s (check %s and %s)",
			client.DisabledReason(), kombifywallet.EnvRecipientKey, kombifywallet.EnvRecipientGeneration)
	}
	public := client.RecipientPublicJWK()
	fmt.Fprintf(stdout, "recipient key valid\ngeneration=%s\npublic_thumbprint=%s\n", generation, public.Thumbprint())
	return nil
}
