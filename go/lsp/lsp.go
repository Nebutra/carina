// Package lsp is a minimal Language Server Protocol client used to collect
// semantic diagnostics (type errors, undefined symbols) for a single edited
// file — beyond what a syntax probe catches. It speaks JSON-RPC 2.0 over the
// LSP stdio framing (Content-Length headers), does the initialize / didOpen
// handshake, and returns the first publishDiagnostics for the opened file.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Diagnostic is one problem reported by the language server (1-based line).
type Diagnostic struct {
	Line     int
	Severity string // error | warning | info | hint
	Message  string
}

// Diagnose spawns a language server, opens filePath, and returns the diagnostics
// it publishes. It returns an error if the server binary is not found or no
// diagnostics arrive before the timeout.
func Diagnose(bin string, args []string, rootDir, filePath, languageID, content string, timeout time.Duration) ([]Diagnostic, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("lsp: server %q not found", bin)
	}
	cmd := exec.Command(bin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	return collect(stdin, stdout, "file://"+rootDir, "file://"+filePath, languageID, content, timeout)
}

// collect drives the LSP handshake over an arbitrary stream pair (so it is
// testable against a mock server), returning diagnostics for fileURI.
func collect(w io.Writer, r io.Reader, rootURI, fileURI, languageID, text string, timeout time.Duration) ([]Diagnostic, error) {
	if err := writeMsg(w, rpcReq(1, "initialize", map[string]any{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
	})); err != nil {
		return nil, err
	}

	resultCh := make(chan []Diagnostic, 1)
	errCh := make(chan error, 1)
	go func() {
		br := bufio.NewReader(r)
		initialized := false
		for {
			raw, err := readMsg(br)
			if err != nil {
				errCh <- err
				return
			}
			var msg struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(raw, &msg)

			if msg.ID != nil && *msg.ID == 1 && !initialized {
				initialized = true
				_ = writeMsg(w, rpcNotify("initialized", map[string]any{}))
				_ = writeMsg(w, rpcNotify("textDocument/didOpen", map[string]any{
					"textDocument": map[string]any{
						"uri": fileURI, "languageId": languageID, "version": 1, "text": text,
					},
				}))
				continue
			}
			if msg.Method == "textDocument/publishDiagnostics" {
				var p struct {
					URI         string `json:"uri"`
					Diagnostics []struct {
						Severity int    `json:"severity"`
						Message  string `json:"message"`
						Range    struct {
							Start struct {
								Line int `json:"line"`
							} `json:"start"`
						} `json:"range"`
					} `json:"diagnostics"`
				}
				if json.Unmarshal(msg.Params, &p) == nil && p.URI == fileURI {
					out := make([]Diagnostic, 0, len(p.Diagnostics))
					for _, d := range p.Diagnostics {
						out = append(out, Diagnostic{
							Line:     d.Range.Start.Line + 1,
							Severity: severityName(d.Severity),
							Message:  d.Message,
						})
					}
					resultCh <- out
					return
				}
			}
		}
	}()

	select {
	case out := <-resultCh:
		return out, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("lsp: timed out waiting for diagnostics")
	}
}

func rpcReq(id int, method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

func rpcNotify(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
}

func severityName(s int) string {
	switch s {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "info"
	}
}

// writeMsg frames a JSON-RPC message with the LSP Content-Length header.
func writeMsg(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// readMsg reads one Content-Length-framed JSON-RPC message.
func readMsg(r *bufio.Reader) (json.RawMessage, error) {
	contentLen := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			n, err := strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
			if err == nil {
				contentLen = n
			}
		}
	}
	if contentLen < 0 {
		return nil, fmt.Errorf("lsp: message without Content-Length")
	}
	buf := make([]byte, contentLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return json.RawMessage(buf), nil
}
