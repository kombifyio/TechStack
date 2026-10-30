package localdb

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/internal/privatefs"
)

type ApplicationConnection struct {
	Version          int    `json:"version"`
	DSN              string `json:"dsn"`
	PhysicalIdentity string `json:"physical_identity"`
	Generation       string `json:"generation"`
}

func EmbeddedBaseDir(dataDir string) string {
	if configured := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresDir)); configured != "" {
		return configured
	}
	return filepath.Join(dataDir, "postgres")
}

// PublishApplicationConnection gives a separate operator terminal only bounded
// application custody. No HTTP handler serves this private record.
func PublishApplicationConnection(dataDir, dsn, identity string) (func(), error) {
	root, err := privatefs.Open(filepath.Join(EmbeddedBaseDir(dataDir), "application"))
	if err != nil {
		return nil, err
	}
	generation, err := randomCredential()
	if err != nil {
		root.Close()
		return nil, err
	}
	record := ApplicationConnection{Version: 1, DSN: dsn, PhysicalIdentity: identity, Generation: generation}
	raw, err := json.Marshal(record)
	if err != nil {
		root.Close()
		return nil, err
	}
	if err := privatefs.Replace(root, "connection.json", append(raw, '\n'), 0o600); err != nil {
		root.Close()
		return nil, err
	}
	return func() {
		defer root.Close()
		raw, err := privatefs.ReadFile(root, "connection.json")
		var current ApplicationConnection
		if err == nil && json.Unmarshal(raw, &current) == nil && current.Generation == generation {
			_ = root.Remove("connection.json")
		}
	}, nil
}

func ReadApplicationConnection(dataDir string) (ApplicationConnection, error) {
	var record ApplicationConnection
	path := filepath.Join(EmbeddedBaseDir(dataDir), "application")
	if _, err := os.Stat(path); err != nil {
		return record, errors.New("native application connection unavailable; start the existing installation first")
	}
	root, err := privatefs.Open(path)
	if err != nil {
		return record, err
	}
	defer root.Close()
	raw, err := privatefs.ReadFile(root, "connection.json")
	if err != nil || json.Unmarshal(raw, &record) != nil || record.Version != 1 || !validNativeIdentity(record.PhysicalIdentity) || len(record.Generation) != 64 {
		return ApplicationConnection{}, errors.New("native application connection record unavailable or invalid")
	}
	u, err := url.Parse(record.DSN)
	if err != nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || u.User == nil || u.User.Username() != NativeApplicationRole || u.Path != "/"+embeddedDatabase {
		return ApplicationConnection{}, errors.New("native application connection is not the bounded local principal")
	}
	return record, nil
}
