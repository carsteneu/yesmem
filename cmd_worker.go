package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/itchyny/gojq"
)

type workerCaller interface {
	Call(method string, params map[string]any) (json.RawMessage, error)
	Close() error
}

type workerRequest struct {
	ID       string          `json:"id,omitempty"`
	Op       string          `json:"op"`
	Params   json.RawMessage `json:"params,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Filter   string          `json:"filter,omitempty"`
	Args     []string        `json:"args,omitempty"`
	StdinLen int             `json:"stdin_len,omitempty"`
}

type workerResponse struct {
	ID    string          `json:"id,omitempty"`
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value,omitempty"`
	Error string          `json:"error,omitempty"`
}

func workerLoop(in io.Reader, out io.Writer, caller workerCaller) error {
	br := bufio.NewReader(in)
	bw := bufio.NewWriter(out)
	defer bw.Flush()

	for {
		line, err := br.ReadBytes('\n')
		eof := errors.Is(err, io.EOF)
		if err != nil && !eof {
			return fmt.Errorf("read: %w", err)
		}
		line = bytes.TrimRight(line, "\n")
		if len(line) == 0 {
			if eof {
				return nil
			}
			continue
		}

		var req workerRequest
		if jsonErr := json.Unmarshal(line, &req); jsonErr != nil {
			_ = encodeLine(bw, workerResponse{OK: false, Error: fmt.Sprintf("decode: %v", jsonErr)})
			bw.Flush()
			if eof {
				return nil
			}
			continue
		}

		switch req.Op {
		case "extract":
			value, err := applyExtract(req.Input, req.Filter)
			if err != nil {
				_ = encodeLine(bw, workerResponse{ID: req.ID, OK: false, Error: err.Error()})
			} else {
				fmt.Fprintln(bw, value)
			}
			bw.Flush()
		case "json_cli":
			if err := handleJSONCLI(br, bw, req); err != nil {
				return err
			}
		default:
			resp := dispatchWorker(req, caller)
			_ = encodeLine(bw, resp)
			bw.Flush()
		}

		if eof {
			return nil
		}
	}
}

// handleJSONCLI runs the json_cli op. After the header line (already consumed
// via the line-read), it reads StdinLen raw bytes from br as the filter input,
// runs jsonCLIRun in-process, and writes a length-framed response:
//
//	{"id":...,"ok":true,"exit":N,"output_len":M}\n<M bytes of output>
//
// On error the response sets ok=false and output_len=0 (no body bytes follow).
func handleJSONCLI(br *bufio.Reader, bw *bufio.Writer, req workerRequest) error {
	stdin := make([]byte, req.StdinLen)
	if req.StdinLen > 0 {
		if _, err := io.ReadFull(br, stdin); err != nil {
			return fmt.Errorf("json_cli read stdin: %w", err)
		}
	}
	output, exit, runErr := jsonCLIRun(req.Args, bytes.NewReader(stdin))
	hdr := map[string]any{
		"id":         req.ID,
		"ok":         runErr == nil,
		"exit":       exit,
		"output_len": len(output),
	}
	if runErr != nil {
		hdr["error"] = runErr.Error()
		hdr["output_len"] = 0
		output = nil
	}
	b, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	if _, err := bw.Write(b); err != nil {
		return err
	}
	if err := bw.WriteByte('\n'); err != nil {
		return err
	}
	if len(output) > 0 {
		if _, err := bw.Write(output); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func encodeLine(w io.Writer, resp workerResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func dispatchWorker(req workerRequest, caller workerCaller) workerResponse {
	switch req.Op {
	case "ping":
		return workerResponse{ID: req.ID, OK: true, Value: json.RawMessage(`"pong"`)}
	case "store":
		if caller == nil {
			return workerResponse{ID: req.ID, OK: false, Error: "store op requires daemon connection"}
		}
		var params map[string]any
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return workerResponse{ID: req.ID, OK: false, Error: fmt.Sprintf("params: %v", err)}
			}
		}
		result, err := caller.Call("cap_store", params)
		if err != nil {
			return workerResponse{ID: req.ID, OK: false, Error: err.Error()}
		}
		return workerResponse{ID: req.ID, OK: true, Value: result}
	case "json":
		v, err := workerApplyFilter(req.Input, req.Filter)
		if err != nil {
			return workerResponse{ID: req.ID, OK: false, Error: err.Error()}
		}
		return workerResponse{ID: req.ID, OK: true, Value: v}
	}
	return workerResponse{ID: req.ID, OK: false, Error: fmt.Sprintf("unknown op: %q", req.Op)}
}

func workerApplyFilter(input json.RawMessage, filter string) (json.RawMessage, error) {
	var v any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &v); err != nil {
			return nil, fmt.Errorf("input: %w", err)
		}
	}
	q, err := gojq.Parse(filter)
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}
	iter := q.Run(v)
	val, ok := iter.Next()
	if !ok {
		return json.RawMessage("null"), nil
	}
	if errv, isErr := val.(error); isErr {
		return nil, errv
	}
	return json.Marshal(val)
}

func applyExtract(input json.RawMessage, filter string) (string, error) {
	raw, err := workerApplyFilter(input, filter)
	if err != nil {
		return "", err
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, nil
		}
	}
	return string(raw), nil
}

func runWorker(_ []string) {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".claude", "yesmem")
	client, err := daemon.Dial(dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker: dial daemon:", err)
		os.Exit(2)
	}
	defer client.Close()
	if err := workerLoop(os.Stdin, os.Stdout, client); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}
