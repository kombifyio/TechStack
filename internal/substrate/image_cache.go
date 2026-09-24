package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/xi2/xz"
)

func downloadFilename(storage string, image Image) string {
	if image.Compression == "" {
		return image.Filename()
	}
	// Proxmox supports checksum-verified compressed downloads only in its
	// archive cache. No archive extraction or template installation is invoked.
	return "kombify-" + storage + "-" + image.SHA256 + ".tar.xz"
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func (c *Client) materializeCompressedImage(ctx context.Context, imported ImageImport) error {
	var cfg struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.request(ctx, http.MethodGet, "/storage/"+imported.Storage, nil, &cfg); err != nil {
		return err
	}
	if cfg.Type != "dir" || !filepath.IsAbs(cfg.Path) || !stringsContainsList(cfg.Content, "import") || !stringsContainsList(cfg.Content, "vztmpl") {
		return ErrCapability
	}
	resolved, err := filepath.EvalSymlinks(cfg.Path)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(cfg.Path) {
		return ErrCapability
	}
	sourcePath := filepath.Join(cfg.Path, "template", "cache", downloadFilename(imported.Storage, imported.Image))
	info, err := os.Lstat(sourcePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<30 {
		return ErrCapability
	}
	resolved, err = filepath.EvalSymlinks(sourcePath)
	if err != nil || resolved != sourcePath {
		return ErrCapability
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	// Revalidate original downloaded bytes locally, even if its prior task
	// finished before the Guard restarted. Never trust a filename as a checksum.
	hash := sha256.New()
	if _, err = io.Copy(hash, contextReader{ctx, source}); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != imported.Image.SHA256 {
		return ErrConflict
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader, err := xz.NewReader(contextReader{ctx, source}, 0)
	if err != nil {
		return ErrConflict
	}
	directory := filepath.Join(cfg.Path, "import")
	info, err = os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(directory, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrCapability
	}
	path := filepath.Join(directory, imported.Image.Filename())
	// A retained output is verified against the original decompressed stream;
	// no attacker-controlled receipt or neighbouring file can bless it.
	if existing, err := os.Open(path); err == nil {
		defer existing.Close()
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return ErrConflict
		}
		a, b := sha256.New(), sha256.New()
		if _, err = io.Copy(a, io.LimitReader(reader, 32<<30)); err != nil {
			return err
		}
		if _, err = io.Copy(b, contextReader{ctx, existing}); err != nil {
			return err
		}
		if string(a.Sum(nil)) != string(b.Sum(nil)) {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(directory, ".kombify-image-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(reader, (32<<30)+1))
	if err != nil {
		return err
	}
	if written > 32<<30 {
		return ErrCapability
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// Hard-link publishes once without overwriting an existing artifact.
	if err = os.Link(temporary, path); err != nil {
		return err
	}
	return nil
}
