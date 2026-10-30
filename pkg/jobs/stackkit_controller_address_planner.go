package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const controllerAddressPlannerFile = "stackkits-controller-address-planner.json"
const controllerAddressPlannerSchema = "techstack.controller-address-planner/v1"

// The Windows bundle producer emits this next to the already packaged native
// CLI, after verifying both platform archives. The target binding prevents a
// different native release/catalog from planning routes for the Linux runtime.
type controllerAddressPlannerPin struct {
	SchemaVersion      string              `json:"schemaVersion"`
	LinuxArchiveSHA256 string              `json:"linuxArchiveSha256"`
	Controller         stackkitrelease.Pin `json:"controller"`
}

func controllerAddressPlannerRelease(target stackkitrelease.Release) (stackkitrelease.Release, error) {
	if filepath.IsAbs(target.BinaryPath()) {
		return target, nil
	}
	if target.Receipt().Candidate != nil {
		pin := strings.TrimSpace(os.Getenv(stackKitReleasePinEnv))
		if !filepath.IsAbs(pin) {
			return stackkitrelease.Release{}, fmt.Errorf("candidate planning needs its admitted controller runtime pin")
		}
		release, err := target.CandidateController(filepath.Dir(pin))
		if err != nil {
			return stackkitrelease.Release{}, err
		}
		return *release, nil
	}
	// A bundled Linux runtime names a path inside the Agent bundle, which the
	// controller cannot execute. A Linux controller (the hosted Core image, a
	// Linux lab Core) holds that very archive as its own pinned release and
	// plans with it; a Windows controller has no such release and needs the
	// bundle sidecar below.
	if own, err := configuredPinnedStackKitRelease(); err == nil && own != nil && runsTargetArchive(*own, target) {
		return *own, nil
	}
	dir := strings.TrimSpace(os.Getenv("TECHSTACK_STACKKITS_DIR"))
	if !filepath.IsAbs(dir) {
		return stackkitrelease.Release{}, fmt.Errorf("managed address planning requires an admitted controller bundle")
	}
	return admitControllerAddressPlanner(dir, target.Receipt())
}

// runsTargetArchive reports whether the controller's own pinned release is the
// exact published archive the target runtime was built from: same kit, version,
// release index, platform and archive digest, with an executable the controller
// resolved to an absolute path. The pin loader already requires its platform to
// be the controller's.
func runsTargetArchive(own, target stackkitrelease.Release) bool {
	have, want := own.Receipt(), target.Receipt()
	return filepath.IsAbs(own.BinaryPath()) && have.Kit == want.Kit && have.Version == want.Version &&
		have.Platform == want.Platform && have.ArchiveSHA256 == want.ArchiveSHA256 && have.IndexSHA256 == want.IndexSHA256
}

func admitControllerAddressPlanner(dir string, target stackkitrelease.Receipt) (stackkitrelease.Release, error) {
	var empty stackkitrelease.Release
	path := filepath.Join(dir, controllerAddressPlannerFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 16<<10 {
		return empty, fmt.Errorf("controller address planner custody is unavailable")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed sidecar below configured controller bundle.
	if err != nil {
		return empty, err
	}
	var binding controllerAddressPlannerPin
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF {
		return empty, fmt.Errorf("controller address planner custody is invalid")
	}
	pin := binding.Controller
	if binding.SchemaVersion != controllerAddressPlannerSchema || binding.LinuxArchiveSHA256 != target.ArchiveSHA256 || target.Platform.OS != "linux" ||
		pin.SchemaVersion != stackkitrelease.PinSchemaVersion || pin.Kit != target.Kit || pin.Version != target.Version || pin.IndexSHA256 != target.IndexSHA256 || pin.BinaryPath != "bin/stackkit.exe" {
		return empty, fmt.Errorf("controller address planner differs from the admitted runtime release")
	}
	// Resolve uses the existing immutable artifact-pin verifier, including the
	// native platform and exact executable digest. No PATH lookup or alternate
	// executable from TECHSTACK_STACKKIT_CLI is accepted.
	pin.BinaryPath = filepath.Join(dir, "bin", "stackkit.exe")
	return (stackkitrelease.Cache{}).Resolve(pin)
}
