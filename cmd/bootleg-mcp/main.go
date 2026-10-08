// Command bootleg-mcp serves bootleg as an MCP connector: over stdio, or HTTP at /mcp when PORT is set.
// Over HTTP it requires OAuth sign-in (BOOTLEG_*); BOOTLEG_NO_AUTH=1 drops it and binds 127.0.0.1.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/auth"
	"github.com/moudlajs/bootleg/internal/config"
	"github.com/moudlajs/bootleg/internal/connector"
)

// delay is quicker than the CLI's default since a chat is waiting, but still human-paced.
const delay = 300 * time.Millisecond

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// On stdio, stdout carries the MCP protocol.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(ctx, os.Getenv("PORT")); err != nil {
		fmt.Fprintln(os.Stderr, "bootleg-mcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, port string) error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	api := applemusic.New(&http.Client{Timeout: 30 * time.Second}, applemusic.DefaultBaseURL, cfg.DevToken, cfg.UserToken)
	server := connector.NewServer(&connector.Service{
		API:        api,
		Storefront: cfg.Storefront,
		Delay:      delay,
		Log:        slog.Default(),
	}, buildVersion())

	if port == "" {
		return server.Run(ctx, &sdk.StdioTransport{})
	}
	signIn, err := signInServer()
	if err != nil {
		return fmt.Errorf("sign-in: %w", err)
	}
	addr := ":" + port
	if signIn == nil {
		// Bind 127.0.0.1 without sign-in, so a stray BOOTLEG_NO_AUTH can't expose Apple Music.
		addr = "127.0.0.1:" + port
	}
	return connector.Serve(ctx, addr, connector.HTTPHandler(server, buildVersion(), signIn))
}

// signInServer returns nil only when BOOTLEG_NO_AUTH=1, so a missing secret fails closed.
func signInServer() (*auth.Server, error) {
	if os.Getenv("BOOTLEG_NO_AUTH") == "1" {
		slog.Warn("BOOTLEG_NO_AUTH=1: no sign-in; listening on 127.0.0.1 only")
		return nil, nil
	}
	return auth.New(auth.Config{
		BaseURL:    os.Getenv("BOOTLEG_BASE_URL"),
		Passphrase: os.Getenv("BOOTLEG_PASSPHRASE"),
		SigningKey: []byte(os.Getenv("BOOTLEG_SIGNING_KEY")),
	})
}

// buildVersion is the ldflags version, else the module version for `go install`, else "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}
