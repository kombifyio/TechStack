package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/kombifyio/techstack/pkg/devicereadiness"
)

// `techstack device` is the operator-side executor.
//
// It exists because the device that most needs help is the one the control
// plane cannot reach. The person who can reach it is standing on the same
// network, so the repair runs from their machine, over their connection, under
// their eyes. Nothing here talks to the control plane: the same binary, the
// same catalog, no account required.

// isDeviceMode reports whether the binary was invoked as `techstack device`.
func isDeviceMode(args []string) bool {
	return len(args) > 1 && args[1] == "device"
}

const deviceUsage = `techstack device — prepare a machine that is not enrolled yet

  techstack device probe   --host <address> --user <name> [auth] [options]
  techstack device prepare --host <address> --user <name> [auth] [options]
  techstack device revert  --host <address> --user <name> [auth] --receipt <file>

probe looks at the machine and prints what it found. prepare does the same and
then offers to carry out the fixes that answer it. revert puts back what a
receipt from prepare --json recorded.

Authentication (one of):
  --key <file>          private key; --key-passphrase-prompt if it is encrypted
  --password-prompt     ask for a password, which is never echoed or stored

Options:
  --port <n>            SSH port (default 22)
  --host-key <SHA256:…> pin the key the machine must present
  --apply <id>          carry out this fix; repeat for several, or "all" for
                        every fix that can be carried out safely
  --egress              lend the machine this computer's route while fixing it,
                        limited to the package sources a repair needs
  --yes                 do not ask before carrying out the fixes named above
  --control-plane <host> the Techstack origin to test reachability against
  --json                print the report as JSON instead of prose
`

func runDeviceMode(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, deviceUsage)
		return errors.New("techstack device: say what to do: probe or prepare")
	}

	action := args[0]
	flags := flag.NewFlagSet("techstack device "+action, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() { fmt.Fprint(os.Stderr, deviceUsage) }

	var (
		host            = flags.String("host", "", "address of the machine")
		port            = flags.Int("port", 22, "SSH port")
		user            = flags.String("user", "", "user to connect as")
		keyPath         = flags.String("key", "", "private key file")
		keyPassphrase   = flags.Bool("key-passphrase-prompt", false, "ask for the key passphrase")
		passwordPrompt  = flags.Bool("password-prompt", false, "ask for a password")
		hostKey         = flags.String("host-key", "", "pin the key the machine must present")
		applyIDs        = newRepeatedFlag(flags, "apply", "fix to carry out; repeat, or \"all\"")
		wantEgress      = flags.Bool("egress", false, "lend the machine this computer's route while fixing it")
		assumeYes       = flags.Bool("yes", false, "do not ask before carrying out the named fixes")
		controlPlane    = flags.String("control-plane", "", "Techstack origin to test reachability against")
		asJSON          = flags.Bool("json", false, "print the report as JSON")
		receiptPath     = flags.String("receipt", "", "receipts written by prepare --json, to be reverted")
		packageMirror   = flags.String("package-mirror", "", "package mirror to test reachability against")
		imageRegistryFl = flags.String("image-registry", "", "image registry to test reachability against")
	)

	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch action {
	case "probe", "prepare", "revert":
	default:
		fmt.Fprint(os.Stderr, deviceUsage)
		return fmt.Errorf("techstack device: %q is not something this does", action)
	}
	if strings.TrimSpace(*host) == "" || strings.TrimSpace(*user) == "" {
		return errors.New("techstack device: --host and --user are required")
	}

	auth, err := resolveDeviceAuth(*keyPath, *keyPassphrase, *passwordPrompt)
	if err != nil {
		return err
	}

	policy := devicereadiness.HostKeyPolicy{
		ExpectedFingerprint: strings.TrimSpace(*hostKey),
		Observe:             confirmHostKey(*assumeYes),
	}

	session, err := devicereadiness.Connect(ctx, devicereadiness.Target{
		Host: *host, Port: *port, User: *user,
	}, auth, policy)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	if action == "revert" {
		return revertFromReceipts(ctx, session, *receiptPath)
	}

	env := devicereadiness.ProbeEnvironment{
		ControlPlaneHost:  *controlPlane,
		PackageMirrorHost: *packageMirror,
		ImageRegistryHost: *imageRegistryFl,
	}

	facts, err := session.Probe(ctx, env)
	if err != nil {
		return err
	}
	report := devicereadiness.Evaluate(facts)

	if *asJSON {
		if err := printJSON(report); err != nil {
			return err
		}
	} else {
		printReport(report)
	}

	if action == "probe" {
		return nil
	}

	chosen := selectResolutions(report, applyIDs.values)
	if len(chosen) == 0 {
		if !*asJSON {
			fmt.Println("\nNothing was carried out. Name a fix with --apply, or --apply all.")
		}
		return nil
	}

	if *wantEgress {
		if err := openDeviceEgress(ctx, session, report, *controlPlane, *assumeYes); err != nil {
			return err
		}
	}

	receipts, applyErr := applyChosen(ctx, session, report, chosen, *assumeYes, *asJSON)
	if *asJSON {
		if err := printJSON(receipts); err != nil {
			return err
		}
	}
	if applyErr != nil {
		return applyErr
	}

	// Re-probing is the only honest way to say whether the repair worked. The
	// report before the fix is a claim; this one is the evidence.
	after, err := session.Probe(ctx, env)
	if err != nil {
		return fmt.Errorf("the fixes were carried out but the machine could not be looked at again: %w", err)
	}
	afterReport := devicereadiness.Evaluate(after)
	if *asJSON {
		return printJSON(afterReport)
	}
	fmt.Println("\nAfter the fixes:")
	printReport(afterReport)
	return nil
}

// revertFromReceipts puts back what a previous repair changed. It works from
// the receipts alone, so an operator can undo a repair from a different machine
// than the one that made it, and without this program having remembered
// anything in between.
func revertFromReceipts(ctx context.Context, session *devicereadiness.Session, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("techstack device revert: --receipt names the file prepare --json wrote")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("techstack device revert: cannot read %s: %w", path, err)
	}
	receipts, err := decodeDeviceReceipts(raw)
	if err != nil {
		return fmt.Errorf("techstack device revert: %s is not repair evidence: %w", path, err)
	}

	// Latest first: a repair that made several changes is undone in the
	// opposite order to the one it was applied in.
	//
	// A repair that failed is reverted too, and is in fact the case that needs
	// this most: a fix whose file was written before its command failed leaves
	// the machine in a state nobody chose. What decides whether there is
	// anything to undo is whether files were written, not whether the repair
	// as a whole worked.
	for i := len(receipts) - 1; i >= 0; i-- {
		receipt := receipts[i]
		if len(receipt.Files) == 0 {
			continue
		}
		fmt.Printf("Undoing %s …\n", receipt.ResolutionID)
		if _, err := session.Undo(ctx, receipt); err != nil {
			return err
		}
		fmt.Println("  done")
	}
	return nil
}

// prepare --json writes reports around its receipt array. Decode the entire
// stream before Undo so a malformed trailing document cannot leave a partial
// rollback. Standalone receipt objects and arrays remain valid inputs.
func decodeDeviceReceipts(raw []byte) ([]devicereadiness.Receipt, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var receipts []devicereadiness.Receipt
	found := false
	for {
		var document json.RawMessage
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		document = bytes.TrimSpace(document)
		var batch []devicereadiness.Receipt
		if document[0] == '[' {
			if err := json.Unmarshal(document, &batch); err != nil {
				return nil, err
			}
		} else {
			var header struct {
				SchemaVersion string                    `json:"schemaVersion"`
				ResolutionID  string                    `json:"resolutionId"`
				Readiness     devicereadiness.Readiness `json:"readiness"`
			}
			if err := json.Unmarshal(document, &header); err != nil {
				return nil, err
			}
			if header.ResolutionID == "" && header.SchemaVersion == devicereadiness.SchemaVersion {
				switch header.Readiness {
				case devicereadiness.ReadinessReady, devicereadiness.ReadinessRepairable,
					devicereadiness.ReadinessNeedsOwner, devicereadiness.ReadinessSubstrate:
					continue
				}
			}
			var receipt devicereadiness.Receipt
			if err := json.Unmarshal(document, &receipt); err != nil {
				return nil, err
			}
			batch = []devicereadiness.Receipt{receipt}
		}
		for _, receipt := range batch {
			if receipt.ResolutionID == "" {
				return nil, errors.New("document contains no repair identity")
			}
		}
		found = true
		receipts = append(receipts, batch...)
	}
	if !found {
		return nil, errors.New("no repair receipts found")
	}
	return receipts, nil
}

// repeatedFlag collects a flag given more than once.
type repeatedFlag struct{ values []string }

func newRepeatedFlag(flags *flag.FlagSet, name, usage string) *repeatedFlag {
	collected := &repeatedFlag{}
	flags.Var(collected, name, usage)
	return collected
}

func (r *repeatedFlag) String() string { return strings.Join(r.values, ",") }

func (r *repeatedFlag) Set(value string) error {
	r.values = append(r.values, value)
	return nil
}

func resolveDeviceAuth(keyPath string, passphrasePrompt, passwordPrompt bool) (devicereadiness.Auth, error) {
	auth := devicereadiness.Auth{}
	if keyPath != "" {
		material, err := os.ReadFile(keyPath)
		if err != nil {
			return auth, fmt.Errorf("techstack device: cannot read the key at %s: %w", keyPath, err)
		}
		auth.PrivateKeyPEM = material
		if passphrasePrompt {
			passphrase, err := readSecret("Passphrase for " + keyPath + ": ")
			if err != nil {
				return auth, err
			}
			auth.Passphrase = []byte(passphrase)
		}
	}
	if passwordPrompt {
		password, err := readSecret("Password: ")
		if err != nil {
			return auth, err
		}
		auth.Password = password
	}
	if len(auth.PrivateKeyPEM) == 0 && auth.Password == "" {
		return auth, errors.New("techstack device: give --key or --password-prompt")
	}
	return auth, nil
}

// readSecret takes input without echoing it. A password typed for a machine
// that is not enrolled yet is still a password.
func readSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("techstack device: refusing to read a secret from something that is not a terminal")
	}
	entered, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("techstack device: could not read the secret: %w", err)
	}
	return string(entered), nil
}

// confirmHostKey shows the operator the key the machine presented. There is no
// trust store for a device nobody has enrolled, so the person next to the
// machine is the trust store.
func confirmHostKey(assumeYes bool) func(string) error {
	return func(fingerprint string) error {
		if assumeYes {
			fmt.Fprintf(os.Stderr, "Machine key %s (accepted without asking because of --yes)\n", fingerprint)
			return nil
		}
		fmt.Fprintf(os.Stderr, "This machine presents the key\n  %s\n", fingerprint)
		fmt.Fprint(os.Stderr, "Compare it with the machine itself. Continue? [y/N] ")
		if !readYes() {
			return errors.New("the machine key was not confirmed")
		}
		return nil
	}
}

func readYes() bool {
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// selectResolutions turns what the operator asked for into catalog entries.
// "all" means every fix that can be carried out, never the advice: advice is
// advice because something outside our reach has to change.
func selectResolutions(report devicereadiness.Report, requested []string) []devicereadiness.Resolution {
	if len(requested) == 1 && strings.EqualFold(requested[0], "all") {
		chosen := make([]devicereadiness.Resolution, 0, len(report.Resolutions))
		for _, resolution := range report.Resolutions {
			if resolution.Mode == devicereadiness.ModeApply {
				chosen = append(chosen, resolution)
			}
		}
		return chosen
	}
	chosen := make([]devicereadiness.Resolution, 0, len(requested))
	for _, id := range requested {
		for _, resolution := range report.Resolutions {
			if resolution.ID == id {
				chosen = append(chosen, resolution)
			}
		}
	}
	return chosen
}

func openDeviceEgress(ctx context.Context, session *devicereadiness.Session, report devicereadiness.Report, controlPlane string, assumeYes bool) error {
	policy := devicereadiness.DefaultEgressPolicy(controlPlane)
	if !assumeYes {
		fmt.Println("\nThis lends the machine your computer's route for the length of the repair.")
		fmt.Println("While it is open the machine can reach only:")
		for _, host := range policy.AllowedHosts {
			fmt.Println("  " + host)
		}
		fmt.Print("Open it? [y/N] ")
		if !readYes() {
			return errors.New("the way out was not opened, so the fixes that need it cannot run")
		}
	}
	egress, err := session.OpenEgress(ctx, policy, report.Facts.Proxy.Environment || report.Facts.Proxy.APT)
	if err != nil {
		return err
	}
	fmt.Printf("The machine can reach the listed sources through %s until this finishes.\n", egress.ProxyURL())
	return nil
}

func applyChosen(ctx context.Context, session *devicereadiness.Session, report devicereadiness.Report, chosen []devicereadiness.Resolution, assumeYes, quiet bool) ([]devicereadiness.Receipt, error) {
	params := devicereadiness.Parameters{
		Interface:    firstRepairableInterface(report),
		OperatorTime: time.Now().UTC(),
	}

	receipts := make([]devicereadiness.Receipt, 0, len(chosen))
	for _, resolution := range chosen {
		if !assumeYes && !confirmResolution(resolution) {
			continue
		}
		if !quiet {
			fmt.Printf("\n%s …\n", resolution.Title)
		}
		receipt, err := session.Apply(ctx, resolution.ID, params)
		receipts = append(receipts, receipt)
		if err != nil {
			return receipts, fmt.Errorf("%s: %w", resolution.Title, err)
		}
		if !quiet {
			fmt.Println("  done" + backupNote(receipt))
		}
	}
	return receipts, nil
}

func confirmResolution(resolution devicereadiness.Resolution) bool {
	fmt.Printf("\n%s\n  %s\n", resolution.Title, resolution.Summary)
	for _, line := range resolution.Guidance {
		fmt.Println("  " + line)
	}
	if resolution.RequiresReboot {
		fmt.Println("  This needs the machine to be restarted afterwards.")
	}
	if !resolution.Reversible {
		fmt.Println("  This cannot be undone from its receipt.")
	}
	fmt.Print("Carry it out? [y/N] ")
	return readYes()
}

func backupNote(receipt devicereadiness.Receipt) string {
	for _, file := range receipt.Files {
		if file.BackupPath != "" {
			return " (the previous " + file.Path + " is kept at " + file.BackupPath + ")"
		}
	}
	return ""
}

// firstRepairableInterface picks the interface a fix should act on: a real one,
// preferring the one that is down, because that is the one worth bringing up.
func firstRepairableInterface(report devicereadiness.Report) string {
	primary := report.Facts.PrimaryInterfaces()
	for _, iface := range primary {
		if !strings.EqualFold(iface.OperState, "up") {
			return iface.Name
		}
	}
	if len(primary) > 0 {
		return primary[0].Name
	}
	return ""
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printReport(report devicereadiness.Report) {
	fmt.Println(readinessHeadline(report))
	for _, check := range report.Checks {
		marker := statusMarker(check.Status)
		if check.Status == devicereadiness.StatusPass || check.Status == devicereadiness.StatusSkipped {
			continue
		}
		fmt.Printf("  %s %s\n", marker, check.Summary)
		if check.Detail != "" {
			fmt.Printf("      %s\n", check.Detail)
		}
	}
	if len(report.Resolutions) == 0 {
		return
	}
	fmt.Println("\nWhat would help:")
	for _, resolution := range report.Resolutions {
		verb := "advice"
		if resolution.Mode == devicereadiness.ModeApply {
			verb = "--apply " + resolution.ID
		}
		fmt.Printf("  %-28s %s\n", verb, resolution.Title)
	}
}

func readinessHeadline(report devicereadiness.Report) string {
	switch report.Readiness {
	case devicereadiness.ReadinessReady:
		return "This machine is ready to be enrolled."
	case devicereadiness.ReadinessRepairable:
		return "This machine cannot be enrolled yet, and there is something to try."
	case devicereadiness.ReadinessSubstrate:
		return "This machine is a hypervisor. Register it and create a guest on it rather than installing onto it."
	default:
		return "This machine cannot be enrolled yet, and only its owner can change that."
	}
}

func statusMarker(status devicereadiness.Status) string {
	switch status {
	case devicereadiness.StatusBlocked:
		return "blocked "
	case devicereadiness.StatusWarning:
		return "warning "
	case devicereadiness.StatusUnknown:
		return "unknown "
	default:
		return "        "
	}
}
