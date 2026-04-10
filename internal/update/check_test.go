package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestCheckForUpdate_NewVersionAvailable(t *testing.T) {
	release := githubRelease{
		TagName: "v1.1.0",
		Body:    "Bug fixes and improvements",
		Assets: []githubAsset{
			{Name: "yesmem_1.1.0_linux_amd64.tar.gz", DownloadURL: "https://example.com/yesmem_1.1.0_linux_amd64.tar.gz"},
			{Name: "yesmem_1.1.0_darwin_arm64.tar.gz", DownloadURL: "https://example.com/yesmem_1.1.0_darwin_arm64.tar.gz"},
			{Name: "checksums.txt", DownloadURL: "https://example.com/checksums.txt"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	info, err := checkRelease(srv.URL, "v1.0.0")
	if err != nil {
		t.Fatalf("checkRelease failed: %v", err)
	}
	if !info.Available {
		t.Error("update should be available")
	}
	if info.Version != "v1.1.0" {
		t.Errorf("version = %q, want v1.1.0", info.Version)
	}
}

func TestCheckForUpdate_AlreadyLatest(t *testing.T) {
	release := githubRelease{TagName: "v1.0.0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	info, err := checkRelease(srv.URL, "v1.0.0")
	if err != nil {
		t.Fatalf("checkRelease failed: %v", err)
	}
	if info.Available {
		t.Error("update should NOT be available when versions match")
	}
}

func TestCheckForUpdate_NonSemverCurrent(t *testing.T) {
	release := githubRelease{TagName: "v1.0.0"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	info, err := checkRelease(srv.URL, "7ba6267")
	if err != nil {
		t.Fatalf("checkRelease failed: %v", err)
	}
	if !info.Available {
		t.Error("update should be available when current version is non-semver")
	}
}

func TestAssetName(t *testing.T) {
	name := assetName("1.1.0", "linux", "amd64")
	if name != "yesmem_1.1.0_linux_amd64.tar.gz" {
		t.Errorf("assetName = %q, want yesmem_1.1.0_linux_amd64.tar.gz", name)
	}
}

func TestFindAssetURL(t *testing.T) {
	assets := []githubAsset{
		{Name: "yesmem_1.1.0_linux_amd64.tar.gz", DownloadURL: "https://example.com/linux"},
		{Name: "yesmem_1.1.0_darwin_arm64.tar.gz", DownloadURL: "https://example.com/darwin"},
		{Name: "checksums.txt", DownloadURL: "https://example.com/checksums"},
	}
	url := findAssetURL(assets, "v1.1.0", runtime.GOOS, runtime.GOARCH)
	if url == "" {
		t.Error("should find asset for current OS/Arch")
	}
}
