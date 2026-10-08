// Command bootleg-mcp serves bootleg as an MCP connector for Claude. It
// speaks MCP over stdio for local clients (Claude Code, Claude Desktop), or
// over HTTP at /mcp when PORT is set (Cloud Run).
//
// Apple Music tokens come from AM_DEV_TOKEN, AM_USER_TOKEN and
// AM_STOREFRONT (a .env in the working directory works too, as for the
// CLI). Over HTTP it requires the owner's OAuth sign-in, configured by
// BOOTLEG_BASE_URL, BOOTLEG_PASSPHRASE and BOOTLEG_SIGNING_KEY; it refuses
// to start without them. BOOTLEG_NO_AUTH=1 turns sign-in off for local
// testing only.
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

// delay between Apple requests: a little quicker than the CLI's default,
// since a chat is waiting, still clearly human-paced.
const delay = 300 * time.Millisecond

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// On stdio, stdout carries the MCP protocol; logs go to stderr.
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
	return connector.Serve(ctx, ":"+port, connector.HTTPHandler(server, buildVersion(), signIn))
}

// signInServer builds the OAuth server from the environment. It returns nil
// only when BOOTLEG_NO_AUTH=1, so a missing secret stops the server instead
// of leaving the owner's Apple Music open to anyone.
func signInServer() (*auth.Server, error) {
	if os.Getenv("BOOTLEG_NO_AUTH") == "1" {
		slog.Warn("BOOTLEG_NO_AUTH=1: anyone who can reach this server can change your Apple Music")
		return nil, nil
	}
	return auth.New(auth.Config{
		BaseURL:    os.Getenv("BOOTLEG_BASE_URL"),
		Passphrase: os.Getenv("BOOTLEG_PASSPHRASE"),
		SigningKey: []byte(os.Getenv("BOOTLEG_SIGNING_KEY")),
	})
}

// buildVersion is the ldflags version for release builds, else the module
// version for `go install`, else "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}
