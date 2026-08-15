package frontend

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

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
