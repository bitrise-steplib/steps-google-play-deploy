package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeZip(t *testing.T, path string, entries ...string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create %s: %s", path, err)
	}
	defer file.Close() //nolint:errcheck

	archive := zip.NewWriter(file)
	for _, entry := range entries {
		if _, err := archive.Create(entry); err != nil {
			t.Fatalf("failed to add %s: %s", entry, err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("failed to close archive: %s", err)
	}
}

func Test_bundleEmbedsMappingFile(t *testing.T) {
	tmpDir := t.TempDir()

	minified := filepath.Join(tmpDir, "app-release.aab")
	writeZip(t, minified, "BundleConfig.pb", "base/manifest/AndroidManifest.xml", bundleMappingEntry)

	notMinified := filepath.Join(tmpDir, "app-debug.aab")
	writeZip(t, notMinified, "BundleConfig.pb", "base/manifest/AndroidManifest.xml")

	apk := filepath.Join(tmpDir, "app-release.apk")
	writeZip(t, apk, "AndroidManifest.xml", "classes.dex")

	notAnArchive := filepath.Join(tmpDir, "broken.aab")
	if err := os.WriteFile(notAnArchive, []byte("not a zip"), 0600); err != nil {
		t.Fatalf("failed to write %s: %s", notAnArchive, err)
	}

	tests := []struct {
		name    string
		appPath string
		want    bool
		wantErr bool
	}{
		{
			name:    "bundle with an embedded mapping file",
			appPath: minified,
			want:    true,
		},
		{
			name:    "bundle without an embedded mapping file",
			appPath: notMinified,
			want:    false,
		},
		{
			// only bundles carry their mapping file, an APK never does
			name:    "apk",
			appPath: apk,
			want:    false,
		},
		{
			name:    "missing bundle",
			appPath: filepath.Join(tmpDir, "nonexistent.aab"),
			want:    false,
			wantErr: true,
		},
		{
			// fail open: an unreadable bundle must not silently drop the upload
			name:    "bundle that is not an archive",
			appPath: notAnArchive,
			want:    false,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bundleEmbedsMappingFile(tt.appPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("bundleEmbedsMappingFile() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("bundleEmbedsMappingFile() = %v, want %v", got, tt.want)
			}
		})
	}
}
