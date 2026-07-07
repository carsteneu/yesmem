package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	platforms := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "https://example.com/linux_amd64"},
		{"linux", "arm64", "https://example.com/linux_arm64"},
		{"darwin", "amd64", "https://example.com/darwin_amd64"},
		{"darwin", "arm64", "https://example.com/darwin_arm64"},
	}
	assets := []githubAsset{
		{Name: "yesmem_1.1.0_linux_amd64.tar.gz", DownloadURL: "https://example.com/linux_amd64"},
		{Name: "yesmem_1.1.0_linux_arm64.tar.gz", DownloadURL: "https://example.com/linux_arm64"},
		{Name: "yesmem_1.1.0_darwin_amd64.tar.gz", DownloadURL: "https://example.com/darwin_amd64"},
		{Name: "yesmem_1.1.0_darwin_arm64.tar.gz", DownloadURL: "https://example.com/darwin_arm64"},
		{Name: "checksums.txt", DownloadURL: "https://example.com/checksums"},
	}
	for _, p := range platforms {
		url := findAssetURL(assets, "v1.1.0", p.goos, p.goarch)
		if url != p.want {
			t.Errorf("findAssetURL(%s, %s) = %q, want %q", p.goos, p.goarch, url, p.want)
		}
	}
}
