package routes

import (
	"strings"
	"unicode"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
)

const bootstrapDigestPrefix = "sha256:"

// managedBootstrapBinding admits only the two secret-free facts written by
// guardbootstrap itself. Free-form registration tags never become lifecycle
// evidence, and raw cloud-init/userData is intentionally not representable.
func managedBootstrapBinding(metadata map[string]any) map[string]any {
	operationID := firstNonEmpty(
		stringFromAny(metadata[guardbootstrap.MetadataOperationID]),
		stringFromAny(metadata["provider_operation_id"]),
	)
	cloudInitSHA256 := strings.ToLower(strings.TrimSpace(stringFromAny(metadata[guardbootstrap.MetadataCloudInitSHA256])))
	if !validBootstrapOperationID(operationID) || !validBootstrapSHA256(cloudInitSHA256) {
		return nil
	}
	return map[string]any{
		"provider_operation_id":                operationID,
		guardbootstrap.MetadataCloudInitSHA256: cloudInitSHA256,
	}
}

func managedBootstrapBindingStrings(metadata map[string]any) (string, string) {
	binding := managedBootstrapBinding(metadata)
	if binding == nil {
		return "", ""
	}
	return stringFromAny(binding["provider_operation_id"]), stringFromAny(binding[guardbootstrap.MetadataCloudInitSHA256])
}

func validBootstrapOperationID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return false
		}
	}
	return true
}

func validBootstrapSHA256(value string) bool {
	if len(value) != len(bootstrapDigestPrefix)+64 || !strings.HasPrefix(value, bootstrapDigestPrefix) {
		return false
	}
	for _, char := range value[len(bootstrapDigestPrefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
