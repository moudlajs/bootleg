package connector

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"

	"github.com/moudlajs/bootleg/internal/auth"
)

// Hosted rate limit per instance for signed-in requests: plenty for one
// person's Claude.
const (
	requestsPerSecond = 5
	requestBurst      = 20
)

// HTTPHandler serves s over stateless Streamable HTTP at /mcp, and /health
// (reporting the version) for health checks; not /healthz, which Cloud Run
// reserves. Stateless because hosted instances come and go. With a non-nil
// signIn, /mcp requires its OAuth access token and its endpoints are served
// too.
func HTTPHandler(s *sdk.Server, version string, signIn *auth.Server) http.Handler {
	mcpHandler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return s },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: slog.Default()},
	)
	// The limit sits inside sign-in, so only signed-in requests spend it:
	// strangers get 401 and can't use up the owner's budget.
	limiter := rate.NewLimiter(requestsPerSecond, requestBurst)
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})

	mux := http.NewServeMux()
	if signIn != nil {
		signIn.Routes(mux)
		h = signIn.Protect(h)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "ok %s\n", version)
	})
	mux.Handle("/mcp", h)
	return mux
}

// Serve serves h on addr until ctx is cancelled, then drains in-flight
// requests (Cloud Run allows 10 s after SIGTERM).
func Serve(ctx context.Context, addr string, h http.Handler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A tool call may take minutes (callTimeout); leave room to answer.
		WriteTimeout: callTimeout + 30*time.Second,
		IdleTimeout:  120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown) // not ctx: it is already cancelled
}
