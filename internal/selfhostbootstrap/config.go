package selfhostbootstrap

import (
	"fmt"
	"os"
	"strings"
)

const Edition = "selfhost-oss"
const DeploymentMode = "self-hosted"

type Config struct {
	Address string
	Edition string
	Mode    string
}

func FromEnv() (Config, error) {
	edition := strings.TrimSpace(os.Getenv("KOMBIFY_EDITION"))
	if edition == "" {
		edition = Edition
	}
	if edition != Edition {
		return Config{}, fmt.Errorf("selfhost binary requires KOMBIFY_EDITION=%s", Edition)
	}
	if value := strings.TrimSpace(os.Getenv("DEPLOYMENT_MODE")); value != "" && value != DeploymentMode {
		return Config{}, fmt.Errorf("DEPLOYMENT_MODE is derived from KOMBIFY_EDITION and cannot be %q", value)
	}
	address := strings.TrimSpace(os.Getenv("TECHSTACK_LISTEN_ADDR"))
	if address == "" {
		address = ":8080"
	}
	return Config{Address: address, Edition: edition, Mode: DeploymentMode}, nil
}
