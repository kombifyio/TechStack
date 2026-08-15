package serverprep

import (
	"strings"
	"testing"
)

func TestGetPrepareScript(t *testing.T) {
	script := GetPrepareScript()
	if len(script) == 0 {
		t.Error("expected script content, got empty string")
	}

	if !strings.Contains(script, "#!/bin/bash") {
		t.Error("expected script to start with shebang")
	}

	if !strings.Contains(script, "install_docker") {
		t.Error("expected script to contain install_docker function")
	}
}
