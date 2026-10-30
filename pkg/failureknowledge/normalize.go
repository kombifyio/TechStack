// Package failureknowledge turns the redacted receipt of a failed operation
// into a class A failure occurrence (knowledge-search plan section 5.8): a
// small set of allowlisted structured fields, a template in which every
// variable token is replaced, and a signature that matches the same failure in
// hosted and self-hosted Techstack. A field that is not on the allowlist is
// never emitted; unknown context stays empty and is never guessed.
package failureknowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kombifyio/techstack/pkg/secrets"
)

// NormalizerVersion is part of every signature. Any change to the template
// rules, the vocabulary or the field allowlist is a new version, never an
// edit of an existing one. v2 keeps only vocabulary words in the template.
const NormalizerVersion = "fk-normalizer/v2"

const (
	maxInputBytes    = 4096
	maxTemplateRunes = 512
)

// Context holds the coarse context dimensions. Callers fill only what they
// already know; Normalize drops a value that does not fit its allowlist.
type Context struct {
	StackKitModule   string `json:"stackkit_module,omitempty"`
	StackKitVersion  string `json:"stackkit_version,omitempty"`
	AppVersion       string `json:"app_version,omitempty"`
	OS               string `json:"os,omitempty"`
	OSVersion        string `json:"os_version,omitempty"`
	Arch             string `json:"arch,omitempty"`
	ContainerRuntime string `json:"container_runtime,omitempty"`
	Provider         string `json:"provider,omitempty"`
	DeviceClass      string `json:"device_class,omitempty"`
}

// Input is the redacted receipt of one failed operation.
type Input struct {
	Component string
	Phase     string
	ErrorCode string
	// Text is the operation's error text. It is redacted again and reduced to
	// the template; nothing of it is kept verbatim.
	Text    string
	Context Context
}

// Occurrence is the normalized, allowlisted result.
type Occurrence struct {
	NormalizerVersion string
	Signature         string
	Component         string
	Phase             string
	ErrorCode         string
	ExitStatus        *int
	Template          string
	Context           Context
}

var (
	codeToken    = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]{0,127}$`)
	versionToken = regexp.MustCompile(`^v?[0-9][0-9a-z.+_-]{0,63}$`)
	exitStatus   = regexp.MustCompile(`(?i)\bexit(?:ed)?(?: with)?(?: code| status)[ :=]+(-?[0-9]{1,5})\b`)
)

// Normalize returns the occurrence for one failed operation. It is pure and
// deterministic: the same receipt always yields the same signature.
func Normalize(in Input) Occurrence {
	// Postgres refuses invalid UTF-8, so the cut lands on a rune boundary.
	text := strings.ToValidUTF8(in.Text, "")
	if len(text) > maxInputBytes {
		cut := maxInputBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	text = secrets.Redact(text)
	out := Occurrence{
		NormalizerVersion: NormalizerVersion,
		Component:         allowCode(in.Component),
		Phase:             allowCode(in.Phase),
		ErrorCode:         allowCode(in.ErrorCode),
		Template:          Template(text),
		Context: Context{
			StackKitModule:   allowCode(in.Context.StackKitModule),
			StackKitVersion:  allowVersion(in.Context.StackKitVersion),
			AppVersion:       allowVersion(in.Context.AppVersion),
			OS:               allowCode(in.Context.OS),
			OSVersion:        allowVersion(in.Context.OSVersion),
			Arch:             allowCode(in.Context.Arch),
			ContainerRuntime: allowCode(in.Context.ContainerRuntime),
			Provider:         allowCode(in.Context.Provider),
			DeviceClass:      allowCode(in.Context.DeviceClass),
		},
	}
	if match := exitStatus.FindStringSubmatch(text); match != nil {
		if status, err := strconv.Atoi(match[1]); err == nil {
			out.ExitStatus = &status
		}
	}
	out.Signature = Signature(out.NormalizerVersion, out.Component, out.Phase, out.ErrorCode, out.Template)
	return out
}

// Signature is sha256 over the normalizer version, component, phase, error
// code and template, encoded as one JSON array so no field can bleed into the
// next.
func Signature(version, component, phase, errorCode, template string) string {
	encoded, _ := json.Marshal([]string{version, component, phase, errorCode, template})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func allowCode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !codeToken.MatchString(value) {
		return ""
	}
	return value
}

func allowVersion(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !versionToken.MatchString(value) {
		return ""
	}
	return value
}
