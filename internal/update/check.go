package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	defaultReleaseURL = "https://api.github.com/repos/carsteneu/yesmem/releases/latest"
	httpTimeout       = 15 * time.Second
)

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

// UpdateInfo describes an available update.
type UpdateInfo struct {
	Available   bool
	Version     string
	Changelog   string
	BinaryURL   string
	ChecksumURL string
}

// CheckForUpdate checks the GitHub Releases API for a newer version.
func CheckForUpdate(currentVersion string) (*UpdateInfo, error) {
	return checkRelease(defaultReleaseURL, currentVersion)
}

func checkRelease(url, currentVersion string) (*UpdateInfo, error) {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}

	info := &UpdateInfo{
		Version:   release.TagName,
		Changelog: release.Body,
	}

	info.BinaryURL = findAssetURL(release.Assets, release.TagName, runtime.GOOS, runtime.GOARCH)
	info.ChecksumURL = findAssetURL(release.Assets, "", "checksums", "")

	current, currentErr := ParseVersion(currentVersion)
	remote, remoteErr := ParseVersion(release.TagName)

	if remoteErr != nil {
		return info, nil
	}
	if currentErr != nil {
		info.Available = true
		return info, nil
	}
	info.Available = remote.NewerThan(current)
	return info, nil
}

func assetName(version, goos, goarch string) string {
	return fmt.Sprintf("yesmem_%s_%s_%s.tar.gz", version, goos, goarch)
}

func findAssetURL(assets []githubAsset, version, goos, goarch string) string {
	if goos == "checksums" {
		for _, a := range assets {
			if a.Name == "checksums.txt" {
				return a.DownloadURL
			}
		}
		return ""
	}
	v := strings.TrimPrefix(version, "v")
	target := assetName(v, goos, goarch)
	for _, a := range assets {
		if a.Name == target {
			return a.DownloadURL
		}
	}
	return ""
}
