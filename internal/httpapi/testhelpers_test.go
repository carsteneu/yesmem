package httpapi

import (
	"log"
	"os"
	"testing"
)

const testToken = "test-token-abc"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(nil, "127.0.0.1:0", testToken, newTestLogger())
}

func newTestLogger() *log.Logger {
	return log.New(os.Stderr, "", 0)
}
