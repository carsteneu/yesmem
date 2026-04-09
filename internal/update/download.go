package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxDownloadSize = 100 << 20 // 100 MB safety limit

// DownloadAndReplace downloads a binary archive, verifies its checksum,
// extracts the binary, and atomically replaces the destination file.
func DownloadAndReplace(binaryURL, checksumURL, assetFilename, dest string) error {
	client := &http.Client{Timeout: 5 * time.Minute}

	// Download archive
	archiveData, err := download(client, binaryURL)
	if err != nil {
		return fmt.Errorf("download binary: %w", err)
	}

	// Download and verify checksum
	checksumData, err := download(client, checksumURL)
	if err != nil {
		return fmt.Errorf("download checksum: %w", err)
	}
	if err := verifyChecksum(archiveData, checksumData, assetFilename); err != nil {
		return err
	}

	// Extract binary from tar.gz
	binary, err := extractBinary(archiveData)
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	return atomicReplace(dest, binary)
}

func download(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
}

func verifyChecksum(data, checksumFile []byte, filename string) error {
	actual := sha256.Sum256(data)
	actualHex := fmt.Sprintf("%x", actual)
	for _, line := range strings.Split(string(checksumFile), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == filename {
			if parts[0] != actualHex {
				return fmt.Errorf("checksum mismatch: got %s, want %s", actualHex, parts[0])
			}
			return nil
		}
	}
	return fmt.Errorf("checksum for %s not found", filename)
}

func extractBinary(archiveData []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		return nil, err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == "yesmem" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary \"yesmem\" not found in archive")
}

// atomicReplace writes new binary content to dest, keeping dest+".bak" as backup.
func atomicReplace(dest string, content []byte) error {
	// Backup existing binary via rename (atomic, zero-copy)
	if _, err := os.Stat(dest); err == nil {
		os.Rename(dest, dest+".bak") // best-effort backup
	}

	// Write to temp file in same dir, then rename for atomicity
	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, "yesmem-update-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op if rename succeeded
	}()

	if _, err := tmp.Write(content); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(0755); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	tmp.Close()

	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
