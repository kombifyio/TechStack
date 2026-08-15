//go:build techstack_static_ui

package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embeddedStatic embed.FS

func staticContent() (fs.FS, bool) {
	content, err := fs.Sub(embeddedStatic, "dist")
	if err != nil {
		return nil, false
	}
	return content, true
}
