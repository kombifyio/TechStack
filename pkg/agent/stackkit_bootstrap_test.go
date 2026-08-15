package agent

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallStackKitRuntimeBundleRejectsTraversal(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "bundle.tar.gz")
	file, err := os.Create(bundle)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gz)
	content := []byte("escape")
	if err := tarWriter.WriteHeader(&tar.Header{Name: ".stackkit/../../escape", Mode: 0600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), ".stackkit")
	if err := installStackKitRuntimeBundle(bundle, target); err == nil {
		t.Fatal("traversing bundle was accepted")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), "escape")); !os.IsNotExist(err) {
		t.Fatalf("bundle escaped target: %v", err)
	}
}
