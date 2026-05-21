package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type stubCaller struct {
	calls    []map[string]any
	respond  func(params map[string]any) (json.RawMessage, error)
	closeErr error
}

func (s *stubCaller) Call(method string, params map[string]any) (json.RawMessage, error) {
	s.calls = append(s.calls, params)
	if s.respond != nil {
		return s.respond(params)
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (s *stubCaller) Close() error { return s.closeErr }

func decodeLines(t *testing.T, r io.Reader) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("response not valid JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestWorker_PingOp(t *testing.T) {
	in := strings.NewReader(`{"id":"p1","op":"ping"}` + "\n")
	out := &bytes.Buffer{}
	caller := &stubCaller{}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	resps := decodeLines(t, out)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0]["id"] != "p1" || resps[0]["ok"] != true {
		t.Errorf("ping resp = %v", resps[0])
	}
}

func TestWorker_StoreOpForwardsToCaller(t *testing.T) {
	in := strings.NewReader(`{"id":"s1","op":"store","params":{"action":"query","capability":"telegram","table":"config","where":"key=?","args":["offset"]}}` + "\n")
	out := &bytes.Buffer{}
	caller := &stubCaller{
		respond: func(p map[string]any) (json.RawMessage, error) {
			return json.RawMessage(`{"rows":[{"id":1,"value":"42"}]}`), nil
		},
	}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("caller invoked %d times, want 1", len(caller.calls))
	}
	if caller.calls[0]["capability"] != "telegram" || caller.calls[0]["action"] != "query" {
		t.Errorf("forwarded params = %v", caller.calls[0])
	}
	resps := decodeLines(t, out)
	if len(resps) != 1 || resps[0]["ok"] != true {
		t.Fatalf("resp = %v", resps)
	}
}

func TestWorker_JsonOp(t *testing.T) {
	in := strings.NewReader(`{"id":"j1","op":"json","input":{"a":1,"b":2},"filter":".a"}` + "\n")
	out := &bytes.Buffer{}
	caller := &stubCaller{}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	resps := decodeLines(t, out)
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0]["ok"] != true {
		t.Fatalf("not ok: %v", resps[0])
	}
	if v, _ := resps[0]["value"].(float64); v != 1 {
		t.Errorf("value = %v, want 1", resps[0]["value"])
	}
}

func TestWorker_ExtractOpRawString(t *testing.T) {
	in := strings.NewReader(`{"op":"extract","input":{"rows":[{"value":"42"}]},"filter":".rows[0].value // \"0\""}` + "\n")
	out := &bytes.Buffer{}
	caller := &stubCaller{}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	got := strings.TrimRight(out.String(), "\n")
	if got != "42" {
		t.Errorf("raw output = %q, want %q", got, "42")
	}
}

func TestWorker_ExtractOpRawNumber(t *testing.T) {
	in := strings.NewReader(`{"op":"extract","input":{"count":7},"filter":".count"}` + "\n")
	out := &bytes.Buffer{}
	caller := &stubCaller{}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	got := strings.TrimRight(out.String(), "\n")
	if got != "7" {
		t.Errorf("raw output = %q, want %q", got, "7")
	}
}

func TestWorker_ExtractOpNullDefault(t *testing.T) {
	in := strings.NewReader(`{"op":"extract","input":{},"filter":".missing // \"fallback\""}` + "\n")
	out := &bytes.Buffer{}
	caller := &stubCaller{}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	got := strings.TrimRight(out.String(), "\n")
	if got != "fallback" {
		t.Errorf("raw output = %q, want %q", got, "fallback")
	}
}

func TestWorker_UnknownOpReturnsError(t *testing.T) {
	in := strings.NewReader(`{"id":"x1","op":"bogus"}` + "\n")
	out := &bytes.Buffer{}
	caller := &stubCaller{}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	resps := decodeLines(t, out)
	if len(resps) != 1 || resps[0]["ok"] != false {
		t.Fatalf("expected ok=false, got %v", resps)
	}
	if !strings.Contains(resps[0]["error"].(string), "bogus") {
		t.Errorf("error = %q, want to mention op", resps[0]["error"])
	}
}

func TestWorker_MultipleRequestsOnSameConnection(t *testing.T) {
	body := strings.Join([]string{
		`{"id":"a","op":"ping"}`,
		`{"id":"b","op":"json","input":{"x":10},"filter":".x"}`,
		`{"id":"c","op":"ping"}`,
	}, "\n") + "\n"
	in := strings.NewReader(body)
	out := &bytes.Buffer{}
	caller := &stubCaller{}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	resps := decodeLines(t, out)
	if len(resps) != 3 {
		t.Fatalf("got %d responses, want 3", len(resps))
	}
	ids := []string{resps[0]["id"].(string), resps[1]["id"].(string), resps[2]["id"].(string)}
	if ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Errorf("ids = %v, want [a b c]", ids)
	}
}

func TestWorker_StoreErrorContinuesLoop(t *testing.T) {
	body := strings.Join([]string{
		`{"id":"e","op":"store","params":{"action":"query"}}`,
		`{"id":"ok","op":"ping"}`,
	}, "\n") + "\n"
	in := strings.NewReader(body)
	out := &bytes.Buffer{}
	caller := &stubCaller{
		respond: func(p map[string]any) (json.RawMessage, error) {
			return nil, errors.New("daemon: simulated failure")
		},
	}

	if err := workerLoop(in, out, caller); err != nil {
		t.Fatalf("loop: %v", err)
	}

	resps := decodeLines(t, out)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
	if resps[0]["ok"] != false || resps[1]["ok"] != true {
		t.Errorf("expected [err,ok], got %v", resps)
	}
}

// readJSONCLIFrame consumes one length-framed json_cli response from r:
//
//	{"id":...,"ok":...,"exit":N,"output_len":M}\n<M bytes>
//
// Returns the header map and the raw output bytes.
func readJSONCLIFrame(t *testing.T, r *bufio.Reader) (map[string]any, []byte) {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(bytes.TrimRight(line, "\n"), &hdr); err != nil {
		t.Fatalf("decode header %q: %v", line, err)
	}
	n := 0
	switch v := hdr["output_len"].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	}
	if n == 0 {
		return hdr, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read body (%d bytes): %v", n, err)
	}
	return hdr, buf
}

func TestWorker_JSONCLIOp_RawField(t *testing.T) {
	stdin := `{"foo":"bar"}`
	body := `{"id":"j1","op":"json_cli","args":["-r",".foo"],"stdin_len":` +
		toStr(len(stdin)) + "}\n" + stdin
	in := strings.NewReader(body)
	out := &bytes.Buffer{}

	if err := workerLoop(in, out, &stubCaller{}); err != nil {
		t.Fatalf("loop: %v", err)
	}

	hdr, output := readJSONCLIFrame(t, bufio.NewReader(out))
	if hdr["ok"] != true {
		t.Fatalf("ok=false: %v", hdr)
	}
	if int(hdr["exit"].(float64)) != 0 {
		t.Errorf("exit=%v want 0", hdr["exit"])
	}
	if string(output) != "bar\n" {
		t.Errorf("output=%q want %q", output, "bar\n")
	}
}

func TestWorker_JSONCLIOp_NullInputBuild(t *testing.T) {
	// stdin_len=0 — null-input mode builds JSON from args
	body := `{"id":"j2","op":"json_cli","args":["-n","--arg","k","hi","-c","{k:$k}"],"stdin_len":0}` + "\n"
	in := strings.NewReader(body)
	out := &bytes.Buffer{}

	if err := workerLoop(in, out, &stubCaller{}); err != nil {
		t.Fatalf("loop: %v", err)
	}

	hdr, output := readJSONCLIFrame(t, bufio.NewReader(out))
	if hdr["ok"] != true {
		t.Fatalf("ok=false: %v", hdr)
	}
	want := `{"k":"hi"}` + "\n"
	if string(output) != want {
		t.Errorf("output=%q want %q", output, want)
	}
}

func TestWorker_JSONCLIOp_ExitStatus(t *testing.T) {
	// -e on null → exit=1, output is "null\n"
	stdin := `{}`
	body := `{"id":"j3","op":"json_cli","args":["-e",".missing"],"stdin_len":` +
		toStr(len(stdin)) + "}\n" + stdin
	in := strings.NewReader(body)
	out := &bytes.Buffer{}

	if err := workerLoop(in, out, &stubCaller{}); err != nil {
		t.Fatalf("loop: %v", err)
	}

	hdr, _ := readJSONCLIFrame(t, bufio.NewReader(out))
	if hdr["ok"] != true {
		t.Fatalf("ok=false: %v", hdr)
	}
	if int(hdr["exit"].(float64)) != 1 {
		t.Errorf("exit=%v want 1", hdr["exit"])
	}
}

func TestWorker_JSONCLIOp_FollowedByPing(t *testing.T) {
	// Ensure the loop correctly resumes after a length-framed body — important
	// because a wrong byte count would consume bytes from the next request.
	stdin := `{"x":1}`
	body := `{"id":"j4","op":"json_cli","args":[".x"],"stdin_len":` + toStr(len(stdin)) + "}\n" +
		stdin +
		"\n" + `{"id":"p","op":"ping"}` + "\n"
	in := strings.NewReader(body)
	out := &bytes.Buffer{}

	if err := workerLoop(in, out, &stubCaller{}); err != nil {
		t.Fatalf("loop: %v", err)
	}

	r := bufio.NewReader(out)
	hdr, output := readJSONCLIFrame(t, r)
	if hdr["ok"] != true {
		t.Fatalf("json_cli ok=false: %v", hdr)
	}
	if strings.TrimSpace(string(output)) != "1" {
		t.Errorf("json_cli output=%q want \"1\\n\"", output)
	}
	// Next line should be the ping response
	rest, _ := io.ReadAll(r)
	if !bytes.Contains(rest, []byte(`"pong"`)) {
		t.Errorf("ping response missing in tail: %q", rest)
	}
}

func toStr(n int) string {
	return fmt.Sprintf("%d", n)
}
