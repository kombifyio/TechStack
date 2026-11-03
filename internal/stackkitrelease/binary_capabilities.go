package stackkitrelease

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
)

// The marker is emitted by the executable's binary-capabilities command. It
// is read from the same bytes whose digest is checked against the exact pin.
const verifyPlanManifest = `stackkit.binary-capabilities/v1:{"capabilities":["stackkit.verify.expected-plan-hash.v1"]}:end-stackkit.binary-capabilities`

const rilPrimitiveManifest = `stackkit.binary-capabilities/v1:{"capabilities":["stackkit.verify.ril-primitive-hash.v1"]}:end-stackkit.binary-capabilities`

var rilPrimitiveHashPattern = regexp.MustCompile(`stackkit\.ril-primitive-contract/v1:(sha256:[a-f0-9]{64}):end-stackkit\.ril-primitive-contract`)

const rilPrimitiveHashMarkerLength = len("stackkit.ril-primitive-contract/v1:sha256:") + 64 + len(":end-stackkit.ril-primitive-contract")

type capabilityScanner struct {
	tail        []byte
	matches     int
	rilMatches  int
	hashMatches int
	hash        string
}

func (s *capabilityScanner) Write(p []byte) (int, error) {
	n := len(p)
	data := append(s.tail, p...)
	countNew := func(marker []byte) int {
		count := 0
		for start := 0; start < len(data); {
			i := bytes.Index(data[start:], marker)
			if i < 0 {
				break
			}
			i += start
			if i+len(marker) > len(s.tail) {
				count++
			}
			start = i + 1
		}
		return count
	}
	s.matches += countNew([]byte(verifyPlanManifest))
	s.rilMatches += countNew([]byte(rilPrimitiveManifest))
	for _, loc := range rilPrimitiveHashPattern.FindAllSubmatchIndex(data, -1) {
		if loc[1] > len(s.tail) {
			s.hashMatches++
			s.hash = string(data[loc[2]:loc[3]])
		}
	}
	keep := len(verifyPlanManifest) - 1
	if n := len(rilPrimitiveManifest) - 1; n > keep {
		keep = n
	}
	if n := rilPrimitiveHashMarkerLength - 1; n > keep {
		keep = n
	}
	if len(data) > keep {
		data = data[len(data)-keep:]
	}
	s.tail = append(s.tail[:0], data...)
	return n, nil
}

// VerifyExpectedPlanSupported is true only when authenticated executable bytes
// from this resolved release carry the source-bound capability declaration.
func (release Release) VerifyExpectedPlanSupported() bool { return release.verifyExpectedPlan }
func (release Release) VerifyRILPrimitiveSupported() bool {
	return release.verifyRILPrimitive && release.rilPrimitiveHash != ""
}
func (release Release) RILPrimitiveHash() string {
	if !release.VerifyRILPrimitiveSupported() {
		return ""
	}
	return release.rilPrimitiveHash
}

// verifyCapabilityBinary accepts only bytes matching the authenticated Guard
// release identity; absent legacy image data never grants the capability.
func verifyCapabilitiesBinary(path, signedDigest string) (bool, bool, string, error) {
	binary, err := digestRegularFile(path, maxExecutableBytes)
	if os.IsNotExist(err) {
		return false, false, "", nil
	}
	if err != nil {
		return false, false, "", fmt.Errorf("verify capability binary: %w", err)
	}
	if binary.sha256 != signedDigest {
		return false, false, "", fmt.Errorf("capability binary differs from signed managed runtime")
	}
	return binary.verifyExpectedPlan, binary.verifyRILPrimitive, binary.rilPrimitiveHash, nil
}

func verifyCapabilityBinary(path, signedDigest string) (bool, error) {
	plan, _, _, err := verifyCapabilitiesBinary(path, signedDigest)
	return plan, err
}

func (s *capabilityScanner) qualifiedHash() string {
	if s.hashMatches != 1 {
		return ""
	}
	return s.hash
}
