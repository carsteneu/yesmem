package update

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFullUpdateFlow(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho 'yesmem v1.1.0'")
	archive := createTarGz(t, "yesmem", binaryContent)
	archiveHash := fmt.Sprintf("%x", sha256.Sum256(archive))
	checksums := fmt.Sprintf("%s  yesmem_1.1.0_linux_amd64.tar.gz\n%s  yesmem_1.1.0_darwin_arm64.tar.gz\n", archiveHash, archiveHash)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			release := githubRelease{
				TagName: "v1.1.0",
				Body:    "## What's New\n- Auto-update support",
				Assets: []githubAsset{
					{Name: "yesmem_1.1.0_linux_amd64.tar.gz", DownloadURL: srv.URL + "/binary"},
					{Name: "yesmem_1.1.0_darwin_arm64.tar.gz", DownloadURL: srv.URL + "/binary"},
					{Name: "checksums.txt", DownloadURL: srv.URL + "/checksums"},
				},
			}
			json.NewEncoder(w).Encode(release)
		case "/binary":
			w.Write(archive)
		case "/checksums":
			w.Write([]byte(checksums))
		}
	}))
	defer srv.Close()

	// Step 1: Check for update
	info, err := checkRelease(srv.URL+"/release", "v1.0.0")
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if !info.Available {
		t.Fatal("update should be available")
	}
	if info.Version != "v1.1.0" {
		t.Errorf("version = %q, want v1.1.0", info.Version)
	}

	// Step 2: Download and replace
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "yesmem")
	os.WriteFile(dest, []byte("old-binary"), 0755)

	asset := assetName("1.1.0", "linux", "amd64")
	err = DownloadAndReplace(info.BinaryURL, info.ChecksumURL, asset, dest)
	if err != nil {
		t.Fatalf("download+replace failed: %v", err)
	}

	// Step 3: Verify new binary
	got, _ := os.ReadFile(dest)
	if string(got) != string(binaryContent) {
		t.Error("binary content mismatch after update")
	}

	// Step 4: Verify backup
	backup, _ := os.ReadFile(dest + ".bak")
	if string(backup) != "old-binary" {
		t.Error("backup should contain old binary")
	}
}

func TestFullUpdateFlow_AlreadyCurrent(t *testing.T) {
	release := githubRelease{TagName: "v1.0.0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	info, err := checkRelease(srv.URL, "v1.0.0")
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if info.Available {
		t.Error("should not offer update when already current")
	}
}
