package appcatalog

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// These raster assets ship with the Techstack application. The native card
// does not depend on an external logo API or an SVG decoder.
const iconBaseURL = "https://techstack.kombify.io/brand-logos/"

//go:embed raster-icons.json
var rasterIconJSON []byte

var rasterIcons = map[string]bool{}

func init() {
	var files []string
	if err := json.Unmarshal(rasterIconJSON, &files); err != nil {
		panic(err)
	}
	for _, file := range files {
		rasterIcons[file] = true
	}
	for i := range entries {
		entries[i].IconURL = IconURL(entries[i].ID)
	}
}

// IconURL returns an owned raster asset, including a generic Kombify icon for
// units whose application logo is not available. It never guesses a vendor.
func IconURL(id string) string {
	file := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), "_", "-")
	if file == "postgres" {
		file = "postgresql"
	}
	if !rasterIcons[file] {
		file = "kombify"
	}
	return iconBaseURL + file + ".png"
}

// GenericEntry labels an unrecognized runtime without claiming catalog
// recognition. Its category remains unknown and its icon is the generic one.
func GenericEntry(name string) *Entry {
	return &Entry{ID: "custom", DisplayName: name, Category: "unknown", IconURL: IconURL("")}
}
