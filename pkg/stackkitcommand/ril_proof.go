package stackkitcommand

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"google.golang.org/protobuf/proto"
)

// RILProofDigest is the content address retained with a sanitized durable command.
func RILProofDigest(proof *agentpb.RILExecutionProof) (string, error) {
	if proof == nil {
		return "", fmt.Errorf("RIL proof required")
	}
	copy := proto.Clone(proof).(*agentpb.RILExecutionProof)
	copy.ProofDigest = ""
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
