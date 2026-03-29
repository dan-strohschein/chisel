package resolve

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// TypeResolver determines the type of a variable at a specific source location.
// Used by the edit phase to disambiguate method calls when the method name is
// shared across multiple types (e.g., Close() on 78 types in Prometheus).
type TypeResolver interface {
	// ResolveType returns the type name of the expression at file:line:column,
	// or "" if the type cannot be determined. The returned type name should be
	// the simple type name (e.g., "Head", "File") without pointer/slice markers.
	ResolveType(file string, line, column int) string

	// Close shuts down the resolver (e.g., stops an LSP server).
	Close()
}

// NullResolver is the default — returns "" for all queries, falling back to
// heuristic-based disambiguation in the edit phase.
type NullResolver struct{}

func (n *NullResolver) ResolveType(file string, line, column int) string { return "" }
func (n *NullResolver) Close()                                          {}

// LSPResolver connects to a Language Server Protocol server to resolve types.
// Works with any LSP server that supports textDocument/hover:
// gopls (Go), pyright (Python), rust-analyzer (Rust), clangd (C/C++),
// typescript-language-server (TS/JS), etc.
type LSPResolver struct {
	cmd     *exec.Cmd
	writer  io.Writer
	reader  *bufio.Reader
	mu      sync.Mutex
	nextID  int64
	rootDir string
	started bool
}

// NewLSPResolver starts an LSP server process and initializes it.
// command is the LSP binary (e.g., "gopls"), args are arguments
// (e.g., ["serve"]), and rootDir is the project root.
func NewLSPResolver(command string, args []string, rootDir string) (*LSPResolver, error) {
	cmd := exec.Command(command, args...)
	cmd.Dir = rootDir
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting LSP server %q: %w", command, err)
	}

	r := &LSPResolver{
		cmd:     cmd,
		writer:  stdinPipe,
		reader:  bufio.NewReaderSize(stdoutPipe, 1024*1024),
		rootDir: rootDir,
	}

	if err := r.initialize(); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("LSP initialize: %w", err)
	}

	r.started = true
	return r, nil
}

func (r *LSPResolver) initialize() error {
	_, err := r.sendRequest("initialize", map[string]interface{}{
		"processId": os.Getpid(),
		"rootUri":   "file://" + r.rootDir,
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"hover": map[string]interface{}{
					"contentFormat": []string{"plaintext"},
				},
			},
		},
	})
	if err != nil {
		return err
	}

	return r.sendNotification("initialized", map[string]interface{}{})
}

// ResolveType queries the LSP server for the type at the given location.
func (r *LSPResolver) ResolveType(file string, line, column int) string {
	if !r.started {
		return ""
	}

	result, err := r.sendRequest("textDocument/hover", map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri": "file://" + file,
		},
		"position": map[string]interface{}{
			"line":      line - 1, // LSP uses 0-based lines
			"character": column - 1,
		},
	})
	if err != nil {
		return ""
	}

	return extractTypeFromHover(result)
}

func (r *LSPResolver) Close() {
	if !r.started {
		return
	}
	r.sendRequest("shutdown", nil)
	r.sendNotification("exit", nil)
	r.cmd.Process.Kill()
	r.cmd.Wait()
}

// sendRequest sends a JSON-RPC request and returns the result.
func (r *LSPResolver) sendRequest(method string, params interface{}) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := atomic.AddInt64(&r.nextID, 1)
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	if err := r.writeMessage(msg); err != nil {
		return nil, fmt.Errorf("writing request: %w", err)
	}

	// Read responses until we get one matching our ID
	for {
		resp, err := r.readMessage()
		if err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}

		// Check if this is our response (has matching id)
		var header struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(resp, &header); err != nil {
			continue
		}

		// Skip notifications (no id)
		if header.ID == nil {
			continue
		}

		if *header.ID != id {
			continue // Not our response
		}

		if header.Error != nil {
			return nil, fmt.Errorf("LSP error %d: %s", header.Error.Code, header.Error.Message)
		}

		return header.Result, nil
	}
}

// sendNotification sends a JSON-RPC notification (no response expected).
func (r *LSPResolver) sendNotification(method string, params interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	return r.writeMessage(msg)
}

// writeMessage writes an LSP message with Content-Length header.
func (r *LSPResolver) writeMessage(msg interface{}) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(r.writer, header); err != nil {
		return err
	}
	_, err = r.writer.Write(body)
	return err
}

// readMessage reads one LSP message from the server.
func (r *LSPResolver) readMessage() (json.RawMessage, error) {
	// Parse headers
	var contentLength int
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			fmt.Sscanf(line, "Content-Length: %d", &contentLength)
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("no Content-Length in LSP response")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r.reader, body); err != nil {
		return nil, err
	}

	return json.RawMessage(body), nil
}

// extractTypeFromHover parses an LSP hover response to extract a type name.
func extractTypeFromHover(result json.RawMessage) string {
	if result == nil || string(result) == "null" {
		return ""
	}

	var hover struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(result, &hover); err != nil {
		return ""
	}

	text := extractHoverText(hover.Contents)
	if text == "" {
		return ""
	}

	// Try gopls patterns:
	// "var h *tsdb.Head" → "Head"
	// "field head *Head" → "Head"
	// "func (h *Head) Close() error" → "Head"
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "var ") || strings.HasPrefix(line, "field ") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				return CleanTypeName(parts[len(parts)-1])
			}
		}

		if strings.HasPrefix(line, "func (") {
			closeIdx := strings.Index(line, ")")
			if closeIdx > 0 {
				recv := line[6:closeIdx]
				parts := strings.Fields(recv)
				if len(parts) >= 2 {
					return CleanTypeName(parts[1])
				}
			}
		}
	}

	return ""
}

// extractHoverText gets the text content from various LSP hover formats.
func extractHoverText(raw json.RawMessage) string {
	// Try as string
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try as MarkupContent: {"kind": "...", "value": "..."}
	var mc struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &mc) == nil && mc.Value != "" {
		return mc.Value
	}

	// Try as MarkedString array
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		for _, item := range arr {
			if t := extractHoverText(item); t != "" {
				return t
			}
		}
	}

	return ""
}

// CleanTypeName removes pointer/slice/channel markers and package prefixes.
// "*tsdb.Head" → "Head", "[]byte" → "byte", "chan *Foo" → "Foo"
func CleanTypeName(t string) string {
	t = strings.TrimLeft(t, "*&[]")
	t = strings.TrimPrefix(t, "chan ")
	t = strings.TrimLeft(t, "*")
	if idx := strings.LastIndex(t, "."); idx >= 0 {
		return t[idx+1:]
	}
	return t
}
