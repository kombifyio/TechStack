package appcatalog

import (
	"slices"
	"strings"
)

// Logos are mirrored to the kombify asset host (Cloudflare R2 bucket
// kombify-public-assets, prefix brand/app-icons/) by cmd/appcatalog-sync:
// homarr-labs dashboard-icons (Apache-2.0) first, Simple Icons (CC0-1.0) as
// the fallback. Nothing is hot-linked from a third party and nothing is served
// by the Techstack web service. Without a mirrored logo the entry carries none
// and the client draws a monogram.
const (
	defaultIconBaseURL       = "https://assets.kombify.io/brand/app-icons/"
	genericIconSlug          = "kombify"
	iconSourceDashboardIcons = "dashboard-icons"
	iconSourceSimpleIcons    = "simple-icons"
	simpleIconsPath          = "simple-icons/"
)

// Logo is the brand mark of a recognized application on the kombify asset
// host. LightURL and DarkURL are the variants for a light or dark UI theme;
// an empty variant means URL fits both.
type Logo struct {
	URL      string `json:"url"`
	LightURL string `json:"light_url,omitempty"`
	DarkURL  string `json:"dark_url,omitempty"`
	Source   string `json:"source"`
}

// logo resolves a mirrored icon slug, preferring the scalable SVG.
func (t *generatedTable) logo(slug string) *Logo {
	asset, ok := t.icons[slug]
	if !ok {
		return nil
	}
	if strings.HasPrefix(slug, simpleIconsPrefix) {
		return &Logo{URL: t.iconBase + simpleIconsPath + strings.TrimPrefix(slug, simpleIconsPrefix) + ".svg", Source: iconSourceSimpleIcons}
	}
	url := t.assetURL(slug, asset, "svg", "webp", "png")
	if url == "" {
		return nil
	}
	logo := &Logo{URL: url, Source: asset.Source}
	if logo.Source != iconSourceDashboardIcons && logo.Source != iconSourceSimpleIcons {
		logo.Source = genericIconSlug
	}
	for _, variant := range []struct {
		slug   string
		target *string
	}{{asset.Light, &logo.LightURL}, {asset.Dark, &logo.DarkURL}} {
		if variantAsset, ok := t.icons[variant.slug]; ok && variant.slug != slug {
			*variant.target = t.assetURL(variant.slug, variantAsset, "svg", "webp", "png")
		}
	}
	return logo
}

// rasterURL is the PNG a native client draws, which carries no SVG decoder;
// an application without one gets the generic kombify mark.
func (t *generatedTable) rasterURL(slug string) string {
	if asset, ok := t.icons[slug]; ok {
		if url := t.assetURL(slug, asset, "png"); url != "" {
			return url
		}
	}
	return genericIconURL(t)
}

func (t *generatedTable) assetURL(slug string, asset iconAsset, formats ...string) string {
	for _, format := range formats {
		if slices.Contains(asset.Formats, format) {
			return t.iconBase + slug + "." + format
		}
	}
	return ""
}

func genericIconURL(t *generatedTable) string {
	return t.iconBase + genericIconSlug + ".png"
}

// IconURL returns the mirrored PNG of an application id, or the generic
// kombify mark when none is mirrored. It never guesses a vendor.
func IconURL(id string) string {
	entry := &Entry{ID: strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), "_", "-")}
	return loadGeneratedTable().rasterURL(entry.iconSlug())
}

// GenericEntry labels an unrecognized runtime without claiming catalog
// recognition. Its category remains unknown, it carries no logo, and its
// icon is the generic one.
func GenericEntry(name string) *Entry {
	return &Entry{ID: "custom", DisplayName: name, Category: "unknown", IconURL: genericIconURL(loadGeneratedTable())}
}
