package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaybeDumpRequestBody_DisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("YESMEM_PROXY_DEBUG", "")

	maybeDumpRequestBody(dir, 42, []byte(`{"hi":1}`))

	if _, err := os.Stat(filepath.Join(logsDir, "req_42_body.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no dump file when YESMEM_PROXY_DEBUG unset, got err=%v", err)
	}
}

func TestMaybeDumpRequestBody_Enabled(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("YESMEM_PROXY_DEBUG", "1")

	maybeDumpRequestBody(dir, 7, []byte(`{"hi":1}`))

	got, err := os.ReadFile(filepath.Join(logsDir, "req_7_body.json"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if string(got) != `{"hi":1}` {
		t.Errorf("dump body mismatch: got %q want %q", got, `{"hi":1}`)
	}
}

func TestMaybeDumpRequestBody_OtherValueIsOff(t *testing.T) {
	dir := t.TempDir()
	logsDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("YESMEM_PROXY_DEBUG", "true")

	maybeDumpRequestBody(dir, 1, []byte("x"))

	if _, err := os.Stat(filepath.Join(logsDir, "req_1_body.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no dump file for YESMEM_PROXY_DEBUG=true (only \"1\" enables), got err=%v", err)
	}
}

func TestMaybeDumpRequestBody_EmptyDataDir(t *testing.T) {
	t.Setenv("YESMEM_PROXY_DEBUG", "1")

	maybeDumpRequestBody("", 99, []byte("x"))
}
