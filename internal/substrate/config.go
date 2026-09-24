package substrate

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

// LoadConfig reads the node-admin configuration, never a control-plane
// credential payload. Symlinks and group/world-writable files are refused.
func LoadConfig(path string) (Config, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 16<<10 {
		return Config{}, errors.New("substrate: protected local configuration required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
