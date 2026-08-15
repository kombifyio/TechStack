package selfhostbootstrap

import (
	_ "embed"
	"encoding/json"
	"net/http"
)

//go:embed release-manifest.json
var releaseManifest []byte

func Handler(config Config) (http.Handler, error) {
	var manifest map[string]any
	if err := json.Unmarshal(releaseManifest, &manifest); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"status": "ok", "edition": config.Edition, "deployment_mode": config.Mode,
		})
	})
	mux.HandleFunc("GET /api/v1/release-manifest", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(manifest)
	})
	return mux, nil
}
