package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// maxStackKitChangeSetRecordBytes bounds the stored change-set record the
// Agent digests after a create.
const maxStackKitChangeSetRecordBytes = 16 << 20

// stackKitAdvancedOperationArgs renders an Advanced operation's argv from the
// stackkit.advanced-operations/v1 catalog of the exact pinned release. The
// capability, and a candidate StackSpec when Core sent one, are written to
// 0600 files inside the workspace's .stackkit directory; the caller removes
// the returned files after the command. No flag is hand-written here.
func stackKitAdvancedOperationArgs(release stackkitrelease.Release, workDir, specPath string, command *agentpb.StackKitCommand) ([]string, []string, error) {
	operation := stackkitcommand.AdvancedCatalogOperation(command.Operation)
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, nil, err
	}
	if dispatchErr := catalog.Dispatchable(operation); dispatchErr != nil {
		return nil, nil, dispatchErr
	}
	if len(command.AdvancedCapability) == 0 || len(command.AdvancedCapability) > stackkitcommand.MaxAdvancedCapabilityBytes {
		return nil, nil, fmt.Errorf("StackKit %s requires one bounded capability", operation)
	}
	var files []string
	fail := func(err error) ([]string, []string, error) {
		removeStackKitTempFiles(files)
		return nil, nil, err
	}
	capabilityPath, err := writeStackKitPrivateFile(workDir, ".techstack-capability-*.json", command.AdvancedCapability)
	if err != nil {
		return fail(fmt.Errorf("write Advanced capability: %w", err))
	}
	files = append(files, capabilityPath)
	values := map[string]string{
		"capabilityFile":    relativeStackKitPath(workDir, capabilityPath),
		"candidateSpecFile": filepath.ToSlash(specPath),
		"changeSetId":       strings.TrimSpace(command.ChangeSetId),
		"changeSetSha256":   strings.TrimSpace(command.ChangeSetSha256),
		"targetRef":         strings.TrimSpace(command.RollbackTargetRef),
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL {
		values["anchorId"] = strings.TrimSpace(command.SnapshotAnchorId)
		values["operationId"] = strings.TrimSpace(command.CommandId)
	}
	if len(command.CandidateSpecJson) > 0 {
		candidatePath, writeErr := writeStackKitPrivateFile(workDir, ".techstack-candidate-*.json", command.CandidateSpecJson)
		if writeErr != nil {
			return fail(fmt.Errorf("write Advanced candidate StackSpec: %w", writeErr))
		}
		files = append(files, candidatePath)
		values["candidateSpecFile"] = relativeStackKitPath(workDir, candidatePath)
	}
	argv, err := catalog.RenderArgv(operation, values)
	if err != nil {
		return fail(err)
	}
	return argv, files, nil
}

// writeStackKitPrivateFile writes data to a new 0600 file below
// <workDir>/.stackkit and returns its absolute path.
func writeStackKitPrivateFile(workDir, pattern string, data []byte) (string, error) {
	directory := filepath.Join(workDir, ".stackkit")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func relativeStackKitPath(workDir, path string) string {
	relative, err := filepath.Rel(workDir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func removeStackKitTempFiles(files []string) {
	for _, file := range files {
		_ = os.Remove(file)
	}
}

// stackKitChangeSetRecordDigest returns the sha256 of the stored change-set
// record a successful create names in data.path, which apply and Advanced
// reconcile pin with --expect-sha256.
func stackKitChangeSetRecordDigest(workDir string, commandResult []byte) (string, error) {
	var envelope struct {
		Data struct {
			Path string `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(commandResult, &envelope); err != nil {
		return "", fmt.Errorf("decode StackKits change-set create result: %w", err)
	}
	relative, err := cleanStackKitRelativePath(envelope.Data.Path, "", "change-set path")
	if err != nil || relative == "" {
		return "", fmt.Errorf("StackKits change-set create result names no workspace record")
	}
	path := filepath.Join(workDir, relative)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxStackKitChangeSetRecordBytes {
		return "", fmt.Errorf("StackKits change-set record must be a bounded regular file")
	}
	file, err := os.Open(path) // #nosec G304 -- path is confined to the validated StackKit workspace.
	if err != nil {
		return "", fmt.Errorf("open StackKits change-set record: %w", err)
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, maxStackKitChangeSetRecordBytes+1)); err != nil {
		return "", fmt.Errorf("digest StackKits change-set record: %w", err)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
