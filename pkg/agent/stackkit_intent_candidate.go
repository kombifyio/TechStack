package agent

import (
	"encoding/json"
	"fmt"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The typed native receipt names exactly the output chosen above. Read only
// that bounded file while runtime custody is locked, then attach its bytes to
// the same command result before the temporary file is removed.
func attachStackKitIntentCandidate(result *agentpb.StackKitResult, tempFiles []string, prefix string) error {
	if len(tempFiles) != 2 || filepath.Base(tempFiles[0]) != "candidate.json" || filepath.Dir(tempFiles[0]) != tempFiles[1] {
		return fmt.Errorf("configuration candidate custody is unavailable")
	}
	var envelope map[string]json.RawMessage
	if result == nil || !result.Success || json.Unmarshal(result.CommandResultJson, &envelope) != nil {
		return fmt.Errorf("configuration authoring failed")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(envelope["data"], &data) != nil {
		return fmt.Errorf("configuration authoring receipt is invalid")
	}
	var output string
	if json.Unmarshal(data["path"], &output) != nil || output != tempFiles[0] {
		return fmt.Errorf("native configuration named another candidate path")
	}
	directoryRef := filepath.Base(tempFiles[1])
	if !strings.HasPrefix(directoryRef, prefix) {
		return fmt.Errorf("configuration candidate directory is invalid")
	}
	root, err := os.OpenRoot(filepath.Dir(tempFiles[1]))
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Lstat(directoryRef)
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("configuration candidate directory is not confined")
	}
	candidateRef := filepath.Join(directoryRef, "candidate.json")
	info, err := root.Lstat(candidateRef)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > stackkitcommand.MaxInitCandidateBytes {
		return fmt.Errorf("configuration candidate is not a bounded regular file")
	}
	file, err := root.Open(candidateRef)
	if err != nil {
		return err
	}
	defer file.Close()
	candidate, err := io.ReadAll(io.LimitReader(file, stackkitcommand.MaxInitCandidateBytes+1))
	if err != nil || len(candidate) > stackkitcommand.MaxInitCandidateBytes || !json.Valid(candidate) {
		return fmt.Errorf("configuration candidate bytes are invalid")
	}
	data["candidate_spec"] = json.RawMessage(candidate)
	envelope["data"], err = json.Marshal(data)
	if err != nil {
		return err
	}
	result.CommandResultJson, err = json.Marshal(envelope)
	if err != nil {
		return err
	}
	return nil
}
