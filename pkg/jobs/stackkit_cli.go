package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const (
	stackKitCLIEnv           = "TECHSTACK_STACKKIT_CLI"
	defaultStackKitCLIBinary = "stackkit"
)

type StackKitCLIGenerator struct {
	Binary       string
	StackKitsDir string
	Timeout      time.Duration
	canonicalV2  bool
	release      *stackkitrelease.Release
}

func NewStackKitCLIGenerator(stackKitsDir string) *StackKitCLIGenerator {
	return &StackKitCLIGenerator{
		Binary:       firstNonEmpty(os.Getenv(stackKitCLIEnv), defaultStackKitCLIBinary),
		StackKitsDir: stackKitsDir,
		Timeout:      runtimeActionHTTPTimeout,
	}
}

// NewBundledStackKitCLIGenerator uses the Windows client's bundled CLI and
// catalog. Unlike the legacy source-workspace constructor, the bundled CLI is
// on the canonical v2 line and must execute the same v2 projection as a pinned
// controller release.
func NewBundledStackKitCLIGenerator(stackKitsDir string) *StackKitCLIGenerator {
	generator := NewStackKitCLIGenerator(stackKitsDir)
	generator.canonicalV2 = true
	return generator
}

// NewPinnedStackKitCLIGenerator creates the artifact generator from an exact
// published release admitted by stackkitrelease.Cache. No StackKits source
// checkout is read or linked into the command workspace.
func NewPinnedStackKitCLIGenerator(release stackkitrelease.Release) *StackKitCLIGenerator {
	return &StackKitCLIGenerator{
		Binary:  release.BinaryPath(),
		Timeout: runtimeActionHTTPTimeout,
		release: &release,
	}
}

func (g *StackKitCLIGenerator) GenerateStackKitArtifacts(ctx context.Context, req StackKitArtifactGenerateRequest) (*StackKitArtifactGenerateResult, error) {
	if g == nil {
		return nil, fmt.Errorf("StackKits CLI generator is not configured")
	}
	workDir, err := cleanRequiredDir(req.WorkDir, "StackKits CLI work directory")
	if err != nil {
		return nil, err
	}
	outputDir, err := cleanRequiredDir(req.OutputDir, "StackKits CLI output directory")
	if err != nil {
		return nil, err
	}
	stackSpecPath := filepath.Clean(strings.TrimSpace(req.StackSpecPath))
	if stackSpecPath == "" {
		return nil, fmt.Errorf("StackKits CLI generation requires stack-spec.yaml")
	}
	if !isPathWithinDir(stackSpecPath, workDir) {
		return nil, fmt.Errorf("stack spec path %q must be inside work directory %q", stackSpecPath, workDir)
	}
	outputRel, err := filepath.Rel(workDir, outputDir)
	if err != nil || outputRel == "." || strings.HasPrefix(outputRel, "..") || filepath.IsAbs(outputRel) {
		return nil, fmt.Errorf("output directory %q must be inside work directory %q", outputDir, workDir)
	}

	stackKit := firstNonEmpty(strings.TrimSpace(req.StackKit), DefaultBaseKitRef)
	if g.release == nil {
		stackKitsDir, err := cleanRequiredDir(g.StackKitsDir, "StackKits source directory")
		if err != nil {
			return nil, err
		}
		if err := ensureStackKitCLIWorkspace(workDir, stackKitsDir, stackKit); err != nil {
			return nil, err
		}
	}

	timeout := g.Timeout
	if timeout <= 0 || timeout > runtimeActionHTTPTimeout {
		timeout = runtimeActionHTTPTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The pinned CLI executes canonical v2 only. A legacy handoff is projected
	// onto the document StackKits authored, beside it rather than over it, so
	// every mechanism that owns stack-spec.yaml keeps working.
	executedSpecPath := stackSpecPath
	derivedCanonicalSpec := false
	canonicalV2 := g.release != nil || g.canonicalV2
	if canonicalV2 {
		canonical, canonicalErr := canonicalStackSpecFor(stackSpecPath, stackKit, req.StackName)
		if canonicalErr != nil {
			return nil, canonicalErr
		}
		executedSpecPath = canonical.Path
		derivedCanonicalSpec = canonical.Derived
		// The destination belongs to the resolved plan, not to the caller:
		// generating into Techstack's own tofu/ directory is refused outright
		// with "architecture v2 --output must resolve to governed ResolvedPlan
		// outputRoot deploy". The result reports the directory back so the
		// rollout reads its artifacts from where they were actually written.
		outputDir = filepath.Join(workDir, canonical.OutputRoot)
		if err := os.MkdirAll(outputDir, 0o750); err != nil {
			return nil, fmt.Errorf("create governed StackKits output directory: %w", err)
		}
		outputRel = canonical.OutputRoot
	}

	commonArgs := []string{
		"--no-log",
		"--chdir", workDir,
		"--spec", filepath.Base(executedSpecPath),
	}
	binary := firstNonEmpty(strings.TrimSpace(g.Binary), defaultStackKitCLIBinary)
	if canonicalV2 {
		validateArgs := append(append([]string(nil), commonArgs...), "validate")
		validate := exec.CommandContext(runCtx, binary, validateArgs...) // #nosec G204 -- binary is admitted from the pinned release; args are fixed.
		validate.Dir = workDir
		validate.Env = os.Environ()
		validateOutput, validateErr := validate.CombinedOutput()
		if validateErr != nil {
			if runCtx.Err() != nil {
				return nil, fmt.Errorf("StackKits CLI validate timed out after %s: %w", timeout, runCtx.Err())
			}
			return nil, fmt.Errorf("StackKits CLI validate failed: %w: %s", validateErr, tailText(string(validateOutput), 4000))
		}
	}

	generateArgs := append(append([]string(nil), commonArgs...),
		"generate",
		"--output", filepath.ToSlash(outputRel),
	)
	if !canonicalV2 {
		// Compatibility-only constructor for existing injected/dev fixtures.
		generateArgs = append(generateArgs, "--force")
	}
	cmd := exec.CommandContext(runCtx, binary, generateArgs...) // #nosec G204 -- binary is admitted from the pinned release; args are fixed.
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if runCtx.Err() != nil {
			return nil, fmt.Errorf("StackKits CLI generate timed out after %s: %w", timeout, runCtx.Err())
		}
		return nil, fmt.Errorf("StackKits CLI generate failed: %w: %s", err, tailText(string(output), 4000))
	}

	metadata := map[string]string{
		"artifact_generator": "stackkit-cli",
		"stackkit":           stackKit,
	}
	resolvedPlanPath := ""
	if canonicalV2 {
		resolvedPlanPath = filepath.Join(outputDir, ".stackkit", "resolved-plan.json")
		resolvedPlanInfo, statErr := os.Lstat(resolvedPlanPath)
		if statErr != nil {
			return nil, fmt.Errorf("StackKits CLI did not persist its canonical ResolvedPlan: %w", statErr)
		}
		if !resolvedPlanInfo.Mode().IsRegular() ||
			resolvedPlanInfo.Mode()&os.ModeSymlink != 0 ||
			resolvedPlanInfo.Size() <= 0 ||
			resolvedPlanInfo.Size() > 32<<20 {
			return nil, fmt.Errorf("StackKits canonical ResolvedPlan must be a non-empty regular file")
		}
		resolvedKit, planHash, identityErr := readStackKitResolvedPlanIdentity(resolvedPlanPath)
		if identityErr != nil {
			return nil, identityErr
		}
		metadata["stackkit"] = resolvedKit
		metadata["resolved_plan_hash"] = planHash
		metadata["executed_stack_spec_path"] = executedSpecPath
		if g.release != nil {
			receipt := g.release.Receipt()
			metadata["validation_authority"] = "pinned-stackkit-cli"
			metadata["generation_authority"] = "pinned-stackkit-cli"
			metadata["release_version"] = receipt.Version
			metadata["release_archive_sha256"] = receipt.ArchiveSHA256
			metadata["release_receipt"] = g.release.ReceiptPath()
		} else {
			metadata["validation_authority"] = "bundled-stackkit-cli"
			metadata["generation_authority"] = "bundled-stackkit-cli"
		}
		if derivedCanonicalSpec {
			metadata["stack_spec_authority"] = "pinned-stackkit-init-template"
		}
	} else {
		metadata["validation_authority"] = "compatibility-fixture"
		metadata["generation_authority"] = "compatibility-fixture"
	}
	return &StackKitArtifactGenerateResult{
		StackSpecPath:    stackSpecPath,
		OutputDir:        outputDir,
		ResolvedPlanPath: resolvedPlanPath,
		Metadata:         metadata,
	}, nil
}

func readStackKitResolvedPlanIdentity(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read StackKits canonical ResolvedPlan: %w", err)
	}
	var identity struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		PlanHash   string `json:"planHash"`
		Kit        struct {
			Slug string `json:"slug"`
		} `json:"kit"`
	}
	if err := json.Unmarshal(data, &identity); err != nil {
		return "", "", fmt.Errorf("decode StackKits canonical ResolvedPlan identity: %w", err)
	}
	if identity.APIVersion != "stackkit.resolved-plan/v1" ||
		identity.Kind != "ResolvedPlan" ||
		strings.TrimSpace(identity.Kit.Slug) == "" ||
		!isSHA256Digest(identity.PlanHash) {
		return "", "", fmt.Errorf("StackKits canonical ResolvedPlan identity is incomplete")
	}
	return strings.TrimSpace(identity.Kit.Slug), identity.PlanHash, nil
}

func isSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func cleanRequiredDir(path, label string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	cleaned, err := filepath.Abs(filepath.Clean(trimmed))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	return cleaned, nil
}

func ensureStackKitCLIWorkspace(workDir, stackKitsDir, stackKit string) error {
	for _, name := range []string{stackKit, "modules", "base", "cue.mod"} {
		target := filepath.Join(stackKitsDir, name)
		if _, err := os.Stat(target); err != nil {
			if name == stackKit || name == "modules" || name == "base" {
				return fmt.Errorf("StackKits workspace source %s missing: %w", target, err)
			}
			continue
		}
		if err := ensureWorkspaceSymlink(workDir, name, target); err != nil {
			return err
		}
	}
	return nil
}

func stackKitCLIWorkspaceReady(stackKitsDir, stackKit string) bool {
	root, err := cleanRequiredDir(stackKitsDir, "StackKits source directory")
	if err != nil {
		return false
	}
	for _, name := range []string{stackKit, "modules", "base"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			return false
		}
	}
	return true
}

func ensureWorkspaceSymlink(workDir, name, target string) error {
	link := filepath.Join(workDir, name)
	if existing, err := os.Lstat(link); err == nil {
		if existing.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		current, readErr := os.Readlink(link)
		if readErr == nil && filepath.Clean(current) == filepath.Clean(target) {
			return nil
		}
		if removeErr := os.Remove(link); removeErr != nil {
			return fmt.Errorf("replace StackKits workspace link %s: %w", link, removeErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect StackKits workspace link %s: %w", link, err)
	}
	if err := os.Symlink(target, link); err != nil {
		copyErr := copyStackKitWorkspaceEntry(target, link)
		if copyErr == nil {
			return nil
		}
		return fmt.Errorf("create StackKits workspace link %s -> %s: %w; copy fallback failed: %v", link, target, err, copyErr)
	}
	return nil
}

func copyStackKitWorkspaceEntry(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyStackKitWorkspaceDir(src, dst, src, dst)
	}
	return copyStackKitWorkspaceFile(src, dst, filepath.Dir(src), filepath.Dir(dst), info.Mode())
}

func copyStackKitWorkspaceDir(src, dst, srcRoot, dstRoot string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err := copyStackKitWorkspaceDir(srcPath, dstPath, srcRoot, dstRoot); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			// Symlinks and other irregular entries could escape the
			// workspace roots when followed; the copy fallback only
			// materializes regular files.
			continue
		}
		if err := copyStackKitWorkspaceFile(srcPath, dstPath, srcRoot, dstRoot, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func copyStackKitWorkspaceFile(src, dst, srcRoot, dstRoot string, mode os.FileMode) error {
	if !isPathWithinDir(src, srcRoot) || !isPathWithinDir(dst, dstRoot) {
		return fmt.Errorf("stackkits workspace copy escapes its root: %s -> %s", src, dst)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

func isPathWithinDir(path, dir string) bool {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func tailText(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= max {
		return trimmed
	}
	return trimmed[len(trimmed)-max:]
}
