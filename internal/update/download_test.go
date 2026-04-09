package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func createTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0755, Typeflag: tar.TypeReg})
	tw.Write(content)
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func TestDownloadAndReplace_Success(t *testing.T) {
	binaryContent := []byte("fake-yesmem-binary-content")
	archive := createTarGz(t, "yesmem", binaryContent)
	hash := sha256.Sum256(archive)
	checksumLine := fmt.Sprintf("%x  yesmem_linux_amd64.tar.gz\n", hash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/binary":
			w.Write(archive)
		case "/checksums":
			w.Write([]byte(checksumLine))
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "yesmem")

	err := DownloadAndReplace(srv.URL+"/binary", srv.URL+"/checksums", "yesmem_linux_amd64.tar.gz", dest)
	if err != nil {
		t.Fatalf("DownloadAndReplace failed: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(binaryContent) {
		t.Error("binary content mismatch")
	}
}

func TestDownloadAndReplace_ChecksumMismatch(t *testing.T) {
	binaryContent := []byte("fake-binary")
	archive := createTarGz(t, "yesmem", binaryContent)
	checksumLine := "0000000000000000000000000000000000000000000000000000000000000000  yesmem_linux_amd64.tar.gz\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/binary":
			w.Write(archive)
		case "/checksums":
			w.Write([]byte(checksumLine))
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "yesmem")

	err := DownloadAndReplace(srv.URL+"/binary", srv.URL+"/checksums", "yesmem_linux_amd64.tar.gz", dest)
	if err == nil {
		t.Fatal("should fail on checksum mismatch")
	}
}

func TestAtomicReplace(t *testing.T) {
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "yesmem")

	os.WriteFile(dest, []byte("old"), 0755)

	err := atomicReplace(dest, []byte("new"))
	if err != nil {
		t.Fatalf("atomicReplace failed: %v", err)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}

	backup, _ := os.ReadFile(dest + ".bak")
	if string(backup) != "old" {
		t.Errorf("backup = %q, want old", backup)
	}
}
