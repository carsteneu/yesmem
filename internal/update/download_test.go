package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"math/rand"
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

func TestDownloadAndReplace_LargeArchive(t *testing.T) {
	// Real goreleaser archives are ~120 MiB; the old 100 MiB download cap
	// truncated them, producing a checksum mismatch on every update.
	const payloadSize = 110 << 20 // above old 100 MiB cap, below new 200 MiB
	// Incompressible payload so the gzip archive stays >100 MiB and actually
	// exercises the download cap (a repetitive pattern would compress tiny).
	rng := rand.New(rand.NewSource(1))
	payload := make([]byte, payloadSize)
	rng.Read(payload)
	binaryContent := append([]byte("fake-yesmem-binary-"), payload...)
	archive := createTarGz(t, "yesmem", binaryContent)
	hash := sha256.Sum256(archive)
	checksumLine := fmt.Sprintf("%x  yesmem_large.tar.gz\n", hash)

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

	err := DownloadAndReplace(srv.URL+"/binary", srv.URL+"/checksums", "yesmem_large.tar.gz", dest)
	if err != nil {
		t.Fatalf("DownloadAndReplace for %d-byte archive failed: %v", len(archive), err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, binaryContent) {
		t.Error("binary content mismatch for large archive")
	}
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
