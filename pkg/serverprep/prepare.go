package serverprep

import (
	_ "embed"
)

//go:embed techstack-prepare.sh
var prepareScript []byte

// GetPrepareScript returns the content of the server preparation script.
func GetPrepareScript() string {
	return string(prepareScript)
}
