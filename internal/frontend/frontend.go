package frontend

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// PublicRuntimeConfigNames are the public browser settings that differ between
// Preview and Stable. They are read from the serving process environment and
// injected into index.html, so one image serves every channel; the build
// (app/vite.config.ts) reads them from window.__TECHSTACK_PUBLIC_CONFIG__.
var PublicRuntimeConfigNames = []string{
	"VITE_GATEWAY_API_BASE",
	"VITE_AUTH0_DOMAIN",
	"VITE_AUTH0_SPA_CLIENT_ID",
	"VITE_AUTH0_AUDIENCE",
}

// publicRuntimeConfigScript returns the inline script that publishes the
// public runtime config. json.Marshal escapes <, > and &, so a value cannot
// close the script element.
func publicRuntimeConfigScript() []byte {
	values := make(map[string]string, len(PublicRuntimeConfigNames))
	for _, name := range PublicRuntimeConfigNames {
		values[name] = strings.TrimSpace(os.Getenv(name))
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	return []byte("<script>window.__TECHSTACK_PUBLIC_CONFIG__=Object.freeze(" + string(encoded) + ");</script>")
}

// withPublicRuntimeConfig places the config script first in <head>, ahead of
// every script that reads it.
func withPublicRuntimeConfig(body []byte) []byte {
	script := publicRuntimeConfigScript()
	marker := []byte("<head>")
	index := bytes.Index(body, marker)
	if script == nil || index < 0 {
		return body
	}
	at := index + len(marker)
	out := make([]byte, 0, len(body)+len(script))
	out = append(out, body[:at]...)
	out = append(out, script...)
	return append(out, body[at:]...)
}

func Handler() (http.Handler, bool) {
	content, ok := staticContent()
	if !ok {
		return nil, false
	}
	return spaHandler{
		content: content,
		files:   http.FileServer(http.FS(content)),
	}, true
}

type spaHandler struct {
	content fs.FS
	files   http.Handler
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	cleanPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if cleanPath == "." || cleanPath == "" {
		h.serveIndex(w, r)
		return
	}

	if fileExists(h.content, cleanPath) {
		h.files.ServeHTTP(w, r)
		return
	}

	if path.Ext(cleanPath) == "" {
		h.serveIndex(w, r)
		return
	}

	http.NotFound(w, r)
}

func (h spaHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	body, err := fs.ReadFile(h.content, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body = withPublicRuntimeConfig(body)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func fileExists(content fs.FS, name string) bool {
	info, err := fs.Stat(content, name)
	return err == nil && !info.IsDir()
}
