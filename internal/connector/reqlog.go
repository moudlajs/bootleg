package connector

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// logRequests logs every /mcp request at its start and end: JSON-RPC
// method(s), client headers, status, duration and whether the response was
// streamed (#72: some requests from Claude stayed open until the timeout).
// It sits inside sign-in, so it only sees the owner's traffic, and never logs
// bodies, parameters or the Authorization header.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		methods := "-"
		if r.Method == http.MethodPost && r.Body != nil {
			// Peek at most 1 MiB, then hand the handler exactly the original
			// stream: what was read, followed by the rest (and any read error).
			peek, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err == nil {
				methods = rpcMethods(peek)
			}
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(peek), r.Body), r.Body}
		}
		attrs := []any{
			"http", r.Method, "rpc", methods,
			"accept", r.Header.Get("Accept"),
			"protocol", r.Header.Get("MCP-Protocol-Version"),
			"session", r.Header.Get("Mcp-Session-Id") != "",
			"last_event", r.Header.Get("Last-Event-ID") != "",
		}
		slog.Info("mcp request start", attrs...)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("mcp request end", append(attrs, "status", sw.status, "streamed", sw.flushed,
			"canceled", r.Context().Err() != nil, "ms", time.Since(start).Milliseconds())...)
	})
}

// rpcMethods names the JSON-RPC messages in a body: "tools/list",
// "initialize,notifications/initialized", or "response" for a client's reply.
func rpcMethods(body []byte) string {
	type msg struct {
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	var batch []msg
	if err := json.Unmarshal(body, &batch); err != nil {
		var one msg
		if err := json.Unmarshal(body, &one); err != nil {
			return "unparsable"
		}
		batch = []msg{one}
	}
	names := make([]string, 0, len(batch))
	for _, m := range batch {
		switch {
		case m.Method != "":
			names = append(names, m.Method)
		case m.Result != nil || m.Error != nil:
			names = append(names, "response")
		default:
			names = append(names, "?")
		}
	}
	return strings.Join(names, ",")
}

// statusWriter records the status and whether the handler flushed (streamed).
type statusWriter struct {
	http.ResponseWriter
	status  int
	flushed bool
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	w.flushed = true
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
