// Command bootleg creates an Apple Music library playlist from a text file
// of "Artist - Title" lines, using the web player's tokens.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/config"
)

// version is overwritten at build time with -ldflags "-X main.version=...".
// It must be a package-level var (not a const) for -X to work.
var version = "dev"

// Exit codes. See the table in README.md and CLAUDE.md; exitCode maps
// errors onto them.
const (
	exitOK    = 0
	exitError = 1 // partial (errPartial), or any other failure
	exitAuth  = 2
	exitInput = 3
)

// authHelp is printed when Apple rejects the tokens. It names the steps and
// the variables, never the token values.
var authHelp = fmt.Sprintf(`Apple Music rejected your web-player tokens; they have probably expired.
Refresh them:
  1. Open https://music.apple.com in a browser and sign in.
  2. Open DevTools (Cmd+Opt+I) > Network, filter on "amp-api", click around.
  3. From any amp-api request's Request Headers copy
       authorization (without "Bearer ")  -> %s
       media-user-token                   -> %s
     into .env or your shell.
See "Token setup" in the README.`, config.EnvDevToken, config.EnvUserToken)

func main() {
	// os.Exit skips deferred calls, so the work happens in realMain, whose
	// defers run before we exit with its result.
	os.Exit(realMain())
}

func realMain() int {
	// Ctrl-C (SIGINT) or SIGTERM cancels ctx. Every request and every wait
	// between requests watches ctx, so the run stops promptly and no
	// playlist is created. stop() restores default signal handling, so a
	// second Ctrl-C kills the process outright.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli(ctx, os.Args[1:], os.Stdout, os.Stderr, applemusic.DefaultBaseURL)
}

// cli parses flags, builds dependencies, calls run and maps its error to an
// exit code. It takes everything it touches as arguments, including the API
// base URL, so tests can drive it end to end against an httptest.Server.
func cli(ctx context.Context, args []string, stdout, stderr io.Writer, baseURL string) int {
	opts, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if errors.Is(err, errFlagsReported) {
		return exitInput // the flag package already printed the error and usage
	}
	if err != nil {
		fmt.Fprintf(stderr, "bootleg: %v\n", err)
		return exitInput
	}
	if opts.showVersion {
		fmt.Fprintln(stdout, resolveVersion(version, debug.ReadBuildInfo))
		return exitOK
	}

	level := slog.LevelInfo
	if opts.verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	if err := config.LoadDotEnv(".env"); err != nil {
		fmt.Fprintf(stderr, "bootleg: %v\n", err)
		return exitInput
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return report(stderr, err)
	}
	if opts.storefront == "" {
		opts.storefront = cfg.Storefront
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	api := applemusic.New(httpClient, baseURL, cfg.DevToken, cfg.UserToken)

	if err := run(ctx, opts, api, stdout, logger); err != nil {
		return report(stderr, err)
	}
	return exitOK
}

// report prints err, plus advice for the errors a user can act on, and
// returns the matching exit code.
func report(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "bootleg: %v\n", err)
	code := exitCode(err)
	switch {
	case errors.Is(err, applemusic.ErrUnauthorized):
		fmt.Fprintln(stderr, authHelp)
	case errors.Is(err, applemusic.ErrRateLimited):
		fmt.Fprintln(stderr, "Apple Music is rate limiting requests. Wait a minute, then run again with a larger -delay (e.g. -delay 2s).")
	case errors.Is(err, errWriteInterrupted):
		// The error text already says to check the library.
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(stderr, "Interrupted before writing; no playlist was created or changed.")
	}
	return code
}

// exitCode maps an error from config or run onto an exit code.
// errors.Is and errors.As look through every %w wrapping layer.
func exitCode(err error) int {
	var pathErr *fs.PathError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, config.ErrMissingToken), errors.Is(err, applemusic.ErrUnauthorized):
		return exitAuth
	case errors.Is(err, errNoSongs), errors.Is(err, errNothingMatched), errors.As(err, &pathErr),
		errors.Is(err, applemusic.ErrNotFound): // a -playlist-id that isn't in the library
		// *fs.PathError: the input file could not be opened or read.
		return exitInput
	default:
		return exitError
	}
}

// resolveVersion returns the -ldflags version for release builds. For
// `go install ...@v1.2.3`, which can't set ldflags, it falls back to the
// module version Go records in the binary. readBuildInfo is a parameter
// (normally debug.ReadBuildInfo) so tests can supply their own.
func resolveVersion(ldflags string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if ldflags != "dev" {
		return ldflags
	}
	// "(devel)" is what a plain `go build` in a checkout records.
	if info, ok := readBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return ldflags
}

// errFlagsReported means flag parsing failed and the flag package has
// already printed the problem and the usage text.
var errFlagsReported = errors.New("invalid flags")

// options are the parsed command-line flags and argument.
// targets are the places matched songs go, from -to.
type targets struct {
	playlist  bool // create (-name) or append to (-playlist-id) a playlist
	library   bool // the library ("Songs")
	favorites bool // the star, i.e. the automatic Favourite Songs playlist
}

// parseTargets reads -to: a comma-separated list of pl, lib and fav, or
// their long forms. Order and repeats don't matter.
func parseTargets(s string) (targets, error) {
	var t targets
	for _, part := range strings.Split(s, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "pl", "playlist":
			t.playlist = true
		case "lib", "library":
			t.library = true
		case "fav", "favs", "favorites", "favourites":
			t.favorites = true
		default:
			return t, fmt.Errorf("-to: unknown destination %q (use pl, lib, fav, or a comma list like lib,fav)", strings.TrimSpace(part))
		}
	}
	return t, nil
}

type options struct {
	to          targets
	name        string
	storefront  string // empty means "use AM_STOREFRONT or its default"
	playlistID  string // append here instead of creating a new playlist
	dryRun      bool
	delay       time.Duration // pause between search requests
	verbose     bool
	showVersion bool
	file        string
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	var to string
	// A FlagSet of our own with ContinueOnError returns errors instead of
	// calling os.Exit(2), which would collide with our auth exit code.
	fset := flag.NewFlagSet("bootleg", flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.StringVar(&to, "to", "pl", "where matched songs go: pl (playlist), lib (Library), fav (Favourite Songs), or a comma list like lib,fav")
	fset.StringVar(&o.name, "name", "", "name of the playlist to create (with -to pl)")
	fset.StringVar(&o.storefront, "storefront", "", "catalog storefront, e.g. cz or us (default $AM_STOREFRONT or us)")
	fset.StringVar(&o.playlistID, "playlist-id", "", "append to this existing library playlist (e.g. p.AbC123) instead of creating one (with -to pl)")
	fset.BoolVar(&o.dryRun, "dry-run", false, "search and show matches, but create nothing")
	fset.DurationVar(&o.delay, "delay", 500*time.Millisecond, "pause between requests (searches, favourites), e.g. 500ms or 2s")
	fset.BoolVar(&o.verbose, "v", false, "verbose (debug) logging")
	fset.BoolVar(&o.showVersion, "version", false, "print version and exit")
	fset.Usage = func() {
		fmt.Fprintln(stderr, `usage: bootleg [-to pl,lib,fav] [-name "Playlist" | -playlist-id ID] [-storefront cz] [-dry-run] [-delay 500ms] [-v] <file.txt>

  bootleg -name "Road trip" songs.txt    new playlist (-to pl is the default)
  bootleg -to lib songs.txt              add to your Library
  bootleg -to fav songs.txt              star them: Favourite Songs (and Library)
  bootleg -to pl,fav -name "Mix" x.txt   playlist and favourites`)
		fmt.Fprintln(stderr)
		fset.PrintDefaults()
	}

	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, err
		}
		return o, errFlagsReported
	}
	if o.showVersion {
		return o, nil
	}
	if fset.NArg() != 1 {
		fset.Usage()
		return o, fmt.Errorf("expected exactly one input file, got %d arguments", fset.NArg())
	}
	o.file = fset.Arg(0)
	if o.delay < 0 {
		return o, errors.New("-delay must not be negative")
	}
	t, err := parseTargets(to)
	if err != nil {
		return o, err
	}
	o.to = t
	if o.name != "" && o.playlistID != "" {
		return o, errors.New("use either -name (create a playlist) or -playlist-id (append to one), not both")
	}
	if !o.to.playlist && (o.name != "" || o.playlistID != "") {
		return o, errors.New("-name and -playlist-id are for playlists; add pl to -to (e.g. -to pl,fav)")
	}
	if o.to.playlist && o.name == "" && o.playlistID == "" && !o.dryRun {
		return o, errors.New("-to pl needs -name or -playlist-id (or use -dry-run, or -to lib / -to fav)")
	}
	return o, nil
}
