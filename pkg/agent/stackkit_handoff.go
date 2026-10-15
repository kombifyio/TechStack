package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// maxHandoffSourceBytes bounds a file the Agent reads for a step; a ResolvedPlan
// is larger than the document extracted from it.
const maxHandoffSourceBytes = 32 << 20

// stackKitHandoffArgs renders one FEDERATION_HANDOFF step as a pinned-CLI
// argv. The incoming public document is written to a 0600 file inside the
// workspace's .stackkit directory, which the caller removes after the
// command. Nothing but the closed step set selects a command.
func stackKitHandoffArgs(workDir string, command *agentpb.StackKitCommand) ([]string, []string, error) {
	step, ok := stackkitcommand.LookupHandoffStep(command.HandoffStep)
	if !ok {
		return nil, nil, fmt.Errorf("StackKit federation hand-off step is not recognized")
	}
	var files []string
	file := ""
	if step.NeedsDocument {
		path, err := writeStackKitPrivateFile(workDir, ".techstack-handoff-*.json", command.HandoffDocument)
		if err != nil {
			return nil, nil, fmt.Errorf("write federation hand-off document: %w", err)
		}
		files = append(files, path)
		file = relativeStackKitPath(workDir, path)
	}
	ref, aux := command.HandoffRef, command.HandoffRef2
	var args []string
	switch step.Name {
	case stackkitcommand.HandoffMemberAdmit:
		args = []string{"fleet", "admit-member", "--node", ref}
	case stackkitcommand.HandoffMemberJoin:
		args = []string{"fleet", "join", file, "--home-key-id", ref}
	case stackkitcommand.HandoffMemberCertify:
		args = []string{"fleet", "certify-member-key", file}
	case stackkitcommand.HandoffMemberImport:
		args = []string{"fleet", "import-member-key", file}
	case stackkitcommand.HandoffBindingAdopt:
		args = []string{"federation", "binding", "adopt", "--binding", file}
	case stackkitcommand.HandoffLinkRequirement:
		// A read-only plan refreshes the node's ResolvedPlan; the requirement
		// is read from it afterwards.
		args = []string{"plan", "--json"}
	case stackkitcommand.HandoffPeerRequest:
		args = []string{"identity", "workload-peer", "request", "--peer-ref", ref, "--owner-approve"}
	case stackkitcommand.HandoffPeerEnroll:
		args = []string{"identity", "workload-peer", "enroll", "--peer-ref", ref, "--file", file, "--server-name", aux, "--owner-approve"}
	case stackkitcommand.HandoffPeerInstall:
		args = []string{"identity", "workload-peer", "install", "--file", file, "--home-root-sha256", aux, "--owner-approve"}
	case stackkitcommand.HandoffLinkBind:
		args = []string{"federation", "link", "bind", "--file", file}
	default:
		removeStackKitTempFiles(files)
		return nil, nil, fmt.Errorf("StackKit federation hand-off step %q has no command", step.Name)
	}
	return args, files, nil
}

// handoffOutputPath is the file the pinned CLI writes a step's document to;
// empty when the document is the command's own stdout.
func handoffOutputPath(command *agentpb.StackKitCommand) string {
	switch command.HandoffStep {
	case stackkitcommand.HandoffMemberAdmit:
		return filepath.Join(".stackkit", "fleet", "member-admissions", command.HandoffRef+".json")
	case stackkitcommand.HandoffMemberJoin:
		return filepath.Join(".stackkit", "fleet", "member-evidence-key-request.json")
	case stackkitcommand.HandoffMemberCertify:
		return filepath.Join(".stackkit", "fleet", "member-evidence-keys", command.HandoffRef+".json")
	case stackkitcommand.HandoffLinkRequirement:
		return filepath.Join("deploy", ".stackkit", "resolved-plan.json")
	}
	return ""
}

// linkRequirementCapability is the one capability the Modern plan binds its
// inter-site link to.
const linkRequirementCapability = "inter-site-link"

// extractLinkRequirement returns the inter-site link requirement of a
// ResolvedPlan.
func extractLinkRequirement(plan []byte) ([]byte, error) {
	var document struct {
		Requirements map[string]json.RawMessage `json:"federationLinkRequirements"`
	}
	if err := json.Unmarshal(plan, &document); err != nil {
		return nil, fmt.Errorf("decode ResolvedPlan: %w", err)
	}
	requirement, ok := document.Requirements[linkRequirementCapability]
	if !ok {
		return nil, errors.New("ResolvedPlan declares no inter-site link requirement")
	}
	return requirement, nil
}

// collectStackKitHandoff reads the public document a successful step produced
// and returns it with the closed command-result envelope that pins its digest.
func collectStackKitHandoff(workDir string, command *agentpb.StackKitCommand, stdout []byte) ([]byte, []byte, error) {
	step, ok := stackkitcommand.LookupHandoffStep(command.HandoffStep)
	if !ok {
		return nil, nil, fmt.Errorf("StackKit federation hand-off step is not recognized")
	}
	data := map[string]any{"schemaVersion": stackkitcommand.HandoffResultVersion, "step": step.Name}
	var document []byte
	if step.ProducesDocument {
		var err error
		if path := handoffOutputPath(command); path != "" {
			document, err = readHandoffDocument(workDir, path)
			if err != nil {
				return nil, nil, err
			}
			if step.Name == stackkitcommand.HandoffLinkRequirement {
				if document, err = extractLinkRequirement(document); err != nil {
					return nil, nil, err
				}
			}
		} else {
			document = []byte(strings.TrimSpace(string(stdout)))
		}
		if len(document) == 0 || len(document) > stackkitcommand.MaxHandoffDocumentBytes || !json.Valid(document) {
			return nil, nil, errors.New("StackKit federation hand-off produced no bounded public JSON document")
		}
		digest := sha256.Sum256(document)
		data["documentSha256"] = "sha256:" + hex.EncodeToString(digest[:])
	}
	if step.Name == stackkitcommand.HandoffMemberAdmit {
		var cli struct {
			Data struct {
				HomeKeyID string `json:"homeKeyId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(stdout, &cli); err != nil || cli.Data.HomeKeyID == "" {
			return nil, nil, errors.New("StackKit member admission did not report the Home owner key id")
		}
		data["homeKeyId"] = cli.Data.HomeKeyID
	}
	envelope, err := json.Marshal(map[string]any{
		"schemaVersion": stackKitCommandResultVersion,
		"command":       stackkitcommand.ResultCommandName(command.Operation),
		"status":        "success",
		"data":          data,
	})
	if err != nil {
		return nil, nil, err
	}
	return document, envelope, nil
}

func readHandoffDocument(workDir, relative string) ([]byte, error) {
	file, err := os.Open(filepath.Join(workDir, relative))
	if err != nil {
		return nil, fmt.Errorf("read federation hand-off document: %w", err)
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, maxHandoffSourceBytes))
	if err != nil {
		return nil, fmt.Errorf("read federation hand-off document: %w", err)
	}
	return document, nil
}
