package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/importer"
)

// fakeAPI is an httptest server that answers search by term and records
// every playlist creation.
type fakeAPI struct {
	mu       sync.Mutex // the handler runs on the server's goroutines
	posts    int
	created  []string // track IDs from the last create request
	appends  int
	appended []string // track IDs from the last append request
	library  []string // song IDs added to the library, in request order
	favs     []string // song IDs favourited, in request order
}

// catalog maps a search term to the songs the fake returns for it.
var catalog = map[string][]map[string]string{
	"Björk Army of Me": {
		{"id": "k1", "name": "Army of Me (Karaoke)", "artistName": "Karaoke Stars"},
		{"id": "b1", "name": "Army of Me", "artistName": "Björk"},
	},
	"Portishead Glory Box": {
		{"id": "p1", "name": "Glory Box", "artistName": "Portishead"},
	},
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/search"):
		var data []map[string]any
		for _, s := range catalog[r.URL.Query().Get("term")] {
			data = append(data, map[string]any{"id": s["id"], "attributes": map[string]string{
				"name": s["name"], "artistName": s["artistName"],
			}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": map[string]any{"songs": map[string]any{"data": data}}})

	case r.Method == http.MethodPost && r.URL.Path == "/v1/me/library":
		f.mu.Lock()
		f.library = append(f.library, strings.Split(r.URL.Query().Get("ids[songs]"), ",")...)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)

	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/me/ratings/songs/"):
		f.mu.Lock()
		f.favs = append(f.favs, strings.TrimPrefix(r.URL.Path, "/v1/me/ratings/songs/"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[]}`))

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tracks"):
		if r.URL.Path != "/v1/me/library/playlists/p.existing/tracks" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Data []struct{ ID string } `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.appends++
		f.appended = nil
		for _, d := range body.Data {
			f.appended = append(f.appended, d.ID)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPost && r.URL.Path == "/v1/me/library/playlists":
		var body struct {
			Relationships struct {
				Tracks struct {
					Data []struct{ ID string } `json:"data"`
				} `json:"tracks"`
			} `json:"relationships"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.posts++
		f.created = nil
		for _, d := range body.Relationships.Tracks.Data {
			f.created = append(f.created, d.ID)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":[{"id":"p.new"}]}`))

	default:
		http.NotFound(w, r)
	}
}

func setup(t *testing.T, input string) (*fakeAPI, *applemusic.Client, string) {
	t.Helper()
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "songs.txt")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user"), path
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const input = "# test\nBjörk - Army of Me\nPortishead - Glory Box\nNobody - Nothing\n"

func TestRunDryRunCreatesNothing(t *testing.T) {
	f, api, path := setup(t, input)
	var out bytes.Buffer

	err := run(context.Background(), options{dryRun: true, storefront: "cz", file: path}, api, &out, discardLogger())
	if !errors.Is(err, errPartial) { // one line has no match
		t.Fatalf("run() error = %v, want errPartial", err)
	}
	if f.posts != 0 {
		t.Errorf("dry run sent %d POST requests, want 0", f.posts)
	}

	got := out.String()
	for _, want := range []string{"LINE", "Björk - Army of Me", "b1", "Portishead - Glory Box", "p1", "Nobody - Nothing", "(no match)"} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "k1") {
		t.Errorf("table picked the karaoke result:\n%s", got)
	}
}

// TestRunPartial: one of three lines is unmatched. The playlist is still
// created from the rest, the line goes to unmatched.txt, and run reports
// errPartial (exit 1).
func TestRunPartial(t *testing.T) {
	f, api, path := setup(t, input)
	var out bytes.Buffer

	err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", storefront: "cz", file: path}, api, &out, discardLogger())
	if !errors.Is(err, errPartial) {
		t.Fatalf("run() error = %v, want errPartial", err)
	}
	if f.posts != 1 {
		t.Fatalf("sent %d POST requests, want 1", f.posts)
	}
	if strings.Join(f.created, ",") != "b1,p1" {
		t.Errorf("created with tracks %v, want [b1 p1] in input order", f.created)
	}

	report := filepath.Join(filepath.Dir(path), unmatchedFile)
	got, readErr := os.ReadFile(report)
	if readErr != nil {
		t.Fatalf("unmatched.txt not written: %v", readErr)
	}
	// Comment header, then the unmatched line verbatim, so the file parses
	// back into exactly that one query.
	if !strings.HasSuffix(string(got), "\nNobody - Nothing\n") || !strings.HasPrefix(string(got), "# ") {
		t.Errorf("unmatched.txt =\n%s", got)
	}
	for _, want := range []string{"Matched 2, unmatched 1, skipped 1", "line 4: Nobody - Nothing", report} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunAllMatchedRemovesStaleReport(t *testing.T) {
	_, api, path := setup(t, "Portishead - Glory Box\n")
	report := filepath.Join(filepath.Dir(path), unmatchedFile)
	if err := os.WriteFile(report, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, io.Discard, discardLogger()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if _, err := os.Stat(report); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale unmatched.txt still there (stat err = %v)", err)
	}
}

// A leftover report that can't be removed (here: a non-empty directory
// with that name) must not stop the playlist being created.
func TestRunStaleReportRemovalFailureIsNotFatal(t *testing.T) {
	f, api, path := setup(t, "Portishead - Glory Box\n")
	blocker := filepath.Join(filepath.Dir(path), unmatchedFile)
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, io.Discard, discardLogger()); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if f.posts != 1 {
		t.Errorf("sent %d POST requests, want 1", f.posts)
	}
}

// Same for a partial run whose report can't be written: the matched songs
// still become a playlist, and the unmatched lines are in the summary.
func TestRunReportWriteFailureIsNotFatal(t *testing.T) {
	f, api, path := setup(t, input)
	blocker := filepath.Join(filepath.Dir(path), unmatchedFile)
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, &out, discardLogger())
	if !errors.Is(err, errPartial) {
		t.Fatalf("run() error = %v, want errPartial", err)
	}
	if f.posts != 1 {
		t.Errorf("sent %d POST requests, want 1", f.posts)
	}
	if !strings.Contains(out.String(), "line 4: Nobody - Nothing") {
		t.Errorf("summary missing the unmatched line:\n%s", out.String())
	}
}

// A create that fails after a partial match still shows the summary and
// leaves the report on disk.
func TestRunCreateFailureStillSummarises(t *testing.T) {
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	api := applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user")
	path := filepath.Join(t.TempDir(), "songs.txt")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, &out, discardLogger())
	if err == nil || errors.Is(err, errPartial) {
		t.Fatalf("run() error = %v, want the create failure", err)
	}
	if !strings.Contains(out.String(), "Matched 2, unmatched 1") {
		t.Errorf("summary missing:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), unmatchedFile)); statErr != nil {
		t.Errorf("report not on disk: %v", statErr)
	}
}

// Re-feeding an unmatched.txt that still has a bad line must not overwrite
// (truncate) the input.
func TestRunPartialNeverOverwritesInput(t *testing.T) {
	f, api, _ := setup(t, "")
	path := filepath.Join(t.TempDir(), unmatchedFile)
	original := "# my notes\nPortishead - Glory Box\nNobody - Nothing\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, io.Discard, discardLogger())
	if !errors.Is(err, errPartial) {
		t.Fatalf("run() error = %v, want errPartial", err)
	}
	if got, _ := os.ReadFile(path); string(got) != original {
		t.Errorf("input was modified:\n%s", got)
	}
	if f.posts != 1 {
		t.Errorf("sent %d POST requests, want 1", f.posts)
	}
}

// Re-feeding unmatched.txt after fixing it must not delete the input.
func TestRunAllMatchedKeepsReportThatIsTheInput(t *testing.T) {
	_, api, _ := setup(t, "")
	path := filepath.Join(t.TempDir(), unmatchedFile)
	if err := os.WriteFile(path, []byte("# fixed\nPortishead - Glory Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, io.Discard, discardLogger()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the input file was removed: %v", err)
	}
}

func TestRunDryRunWritesNoReport(t *testing.T) {
	_, api, path := setup(t, input)
	var out bytes.Buffer

	err := run(context.Background(), options{dryRun: true, file: path}, api, &out, discardLogger())
	if !errors.Is(err, errPartial) {
		t.Fatalf("run() error = %v, want errPartial", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), unmatchedFile)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("dry run wrote unmatched.txt (stat err = %v)", statErr)
	}
	if !strings.Contains(out.String(), "unmatched 1") {
		t.Errorf("dry-run summary missing:\n%s", out.String())
	}
}

func TestRunNothingMatched(t *testing.T) {
	f, api, path := setup(t, "Nobody - Nothing\n")
	report := filepath.Join(filepath.Dir(path), unmatchedFile)
	if err := os.WriteFile(report, []byte("stale from an earlier run\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := run(context.Background(), options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, &out, discardLogger())
	if !errors.Is(err, errNothingMatched) {
		t.Fatalf("run() error = %v, want errNothingMatched", err)
	}
	if !strings.Contains(err.Error(), report) {
		t.Errorf("error %q should name the report", err)
	}
	// The report is refreshed with this run's lines, not left stale.
	got, readErr := os.ReadFile(report)
	if readErr != nil || strings.Contains(string(got), "stale") || !strings.HasSuffix(string(got), "\nNobody - Nothing\n") {
		t.Errorf("unmatched.txt not refreshed (err %v):\n%s", readErr, got)
	}
	if !strings.Contains(out.String(), "line 1: Nobody - Nothing") {
		t.Errorf("summary missing the unmatched line:\n%s", out.String())
	}
	if f.posts != 0 {
		t.Errorf("sent %d POST requests, want 0", f.posts)
	}
}

func TestRunInputErrors(t *testing.T) {
	_, api, emptyPath := setup(t, "# only a comment\n\n")
	missing := filepath.Join(t.TempDir(), "missing.txt")

	for name, path := range map[string]string{"empty": emptyPath, "missing": missing} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), options{dryRun: true, file: path}, api, io.Discard, discardLogger())
			if err == nil {
				t.Fatal("run() error = nil, want an input error")
			}
		})
	}
}

func TestCLIFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"version", []string{"-version"}, exitOK, "dev"},
		{"help", []string{"-h"}, exitOK, ""},
		{"unknown flag", []string{"-nope", "x.txt"}, exitInput, ""},
		{"no file", []string{"-name", "x"}, exitInput, ""},
		{"two files", []string{"-name", "x", "a.txt", "b.txt"}, exitInput, ""},
		{"no name without dry-run", []string{"a.txt"}, exitInput, ""},
		{"name and playlist-id", []string{"-name", "x", "-playlist-id", "p.1", "a.txt"}, exitInput, ""},
		{"unknown -to", []string{"-to", "lib,cloud", "a.txt"}, exitInput, ""},
		{"-name without pl", []string{"-to", "lib", "-name", "x", "a.txt"}, exitInput, ""},
		{"-to pl without a playlist", []string{"-to", "pl,fav", "a.txt"}, exitInput, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := cli(context.Background(), tt.args, &out, io.Discard, "http://unused.invalid"); code != tt.wantCode {
				t.Errorf("cli(%v) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want containing %q", out.String(), tt.wantOut)
			}
		})
	}
}

// TestCLIEndToEnd drives cli() through config loading, the storefront
// fallback and client wiring, against the fake API.
func TestCLIEndToEnd(t *testing.T) {
	f := &fakeAPI{}
	var storefrontPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			f.mu.Lock()
			storefrontPath = r.URL.Path
			f.mu.Unlock()
		}
		f.handler(w, r)
	}))
	t.Cleanup(srv.Close)

	// Run in an empty directory so a developer's real .env is never read.
	t.Chdir(t.TempDir())
	if err := os.WriteFile("songs.txt", []byte("Portishead - Glory Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AM_DEV_TOKEN", "fake-dev")
	t.Setenv("AM_USER_TOKEN", "fake-user")
	t.Setenv("AM_STOREFRONT", "cz")

	var out, errOut bytes.Buffer
	code := cli(context.Background(), []string{"-name", "Mix", "songs.txt"}, &out, &errOut, srv.URL)
	if code != exitOK {
		t.Fatalf("cli() = %d, want %d; stderr:\n%s", code, exitOK, errOut.String())
	}
	if storefrontPath != "/v1/catalog/cz/search" {
		t.Errorf("searched %q, want the AM_STOREFRONT fallback cz", storefrontPath)
	}
	if f.posts != 1 {
		t.Errorf("sent %d POST requests, want 1", f.posts)
	}
	if strings.Contains(errOut.String(), "fake-dev") || strings.Contains(errOut.String(), "fake-user") {
		t.Errorf("stderr leaks a token:\n%s", errOut.String())
	}
}

func TestCLIFlagErrorPrintedOnce(t *testing.T) {
	var errOut bytes.Buffer
	cli(context.Background(), []string{"-nope", "x.txt"}, io.Discard, &errOut, "http://unused.invalid")
	if n := strings.Count(errOut.String(), "-nope"); n != 1 {
		t.Errorf("flag error printed %d times, want 1:\n%s", n, errOut.String())
	}
}

// cliRun runs cli() in a temp dir with input.txt holding input, tokens set
// (unless noTokens), and the API answered by handler. It returns the exit
// code and stderr.
func cliRun(t *testing.T, handler http.HandlerFunc, input string, noTokens bool, args ...string) (int, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	t.Chdir(t.TempDir()) // never read a developer's real .env
	if input != "" {
		if err := os.WriteFile("input.txt", []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"AM_DEV_TOKEN", "AM_USER_TOKEN", "AM_STOREFRONT"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k) // t.Setenv's cleanup restores the original value
	}
	if !noTokens {
		t.Setenv("AM_DEV_TOKEN", "fake-dev")
		t.Setenv("AM_USER_TOKEN", "fake-user")
	}

	var errOut bytes.Buffer
	code := cli(context.Background(), args, io.Discard, &errOut, srv.URL)
	return code, errOut.String()
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func TestCLIExitCodes(t *testing.T) {
	ok := (&fakeAPI{}).handler
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		input      string
		noTokens   bool
		args       []string
		wantCode   int
		wantStderr string
	}{
		{"all matched", ok, "Portishead - Glory Box\n", false, []string{"-name", "x", "input.txt"}, exitOK, ""},
		{"append", ok, "Portishead - Glory Box\n", false, []string{"-playlist-id", "p.existing", "input.txt"}, exitOK, ""},
		{"append to unknown playlist", ok, "Portishead - Glory Box\n", false, []string{"-playlist-id", "p.nope", "input.txt"}, exitInput, "not found"},
		{"partial", ok, "Portishead - Glory Box\nNobody - Nothing\n", false, []string{"-name", "x", "input.txt"}, exitError, "unmatched.txt"},
		{"missing token", ok, "Portishead - Glory Box\n", true, []string{"-name", "x", "input.txt"}, exitAuth, "AM_DEV_TOKEN is not set"},
		{"401", status(http.StatusUnauthorized), "a - b\n", false, []string{"-name", "x", "input.txt"}, exitAuth, "DevTools"},
		{"403", status(http.StatusForbidden), "a - b\n", false, []string{"-dry-run", "input.txt"}, exitAuth, "media-user-token"},
		{"429", status(http.StatusTooManyRequests), "a - b\n", false, []string{"-name", "x", "input.txt"}, exitError, "larger -delay"},
		{"500", status(http.StatusInternalServerError), "a - b\n", false, []string{"-name", "x", "input.txt"}, exitError, "unexpected status 500"},
		{"missing file", ok, "", false, []string{"-name", "x", "nope.txt"}, exitInput, "open input"},
		{"only comments", ok, "# nothing\n\n", false, []string{"-name", "x", "input.txt"}, exitInput, "no songs found"},
		{"nothing matched", ok, "Nobody - Nothing\n", false, []string{"-name", "x", "input.txt"}, exitInput, "nothing to import"},
		{"bad flag", ok, "", false, []string{"-nope"}, exitInput, ""},
		{"negative delay", ok, "", false, []string{"-delay", "-1s", "-dry-run", "input.txt"}, exitInput, "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stderr := cliRun(t, tt.handler, tt.input, tt.noTokens, tt.args...)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d; stderr:\n%s", code, tt.wantCode, stderr)
			}
			if !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantStderr, stderr)
			}
			if strings.Contains(stderr, "fake-dev") || strings.Contains(stderr, "fake-user") {
				t.Errorf("stderr leaks a token:\n%s", stderr)
			}
		})
	}
}

func TestRunWaitsBetweenSearches(t *testing.T) {
	_, api, path := setup(t, "a - 1\nb - 2\nc - 3\n")
	const delay = 40 * time.Millisecond

	start := time.Now()
	// None of these lines match the fake catalog; only the timing matters.
	err := run(context.Background(), options{dryRun: true, delay: delay, file: path}, api, io.Discard, discardLogger())
	if err != nil && !errors.Is(err, errPartial) {
		t.Fatalf("run() error = %v", err)
	}
	// Three searches means two pauses; none before the first.
	if elapsed := time.Since(start); elapsed < 2*delay {
		t.Errorf("run took %v, want at least %v", elapsed, 2*delay)
	}
}

// TestRunCancelledMidRun cancels the context while the first search is in
// flight, as Ctrl-C would, and checks the run stops without creating
// anything - even though the next step is a long -delay.
func TestRunCancelledMidRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := &fakeAPI{}
	var searches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			searches.Add(1)
			cancel() // "Ctrl-C" during the first request
		}
		f.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	api := applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user")

	path := filepath.Join(t.TempDir(), "songs.txt")
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err := run(ctx, options{to: importer.Targets{Playlist: true}, name: "Mix", delay: time.Hour, file: path}, api, io.Discard, discardLogger())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("run did not stop promptly after cancellation")
	}
	if n := searches.Load(); n != 1 {
		t.Errorf("made %d searches, want 1", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Errorf("sent %d POST requests after cancellation, want 0", f.posts)
	}
}

func TestCLIInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv := httptest.NewServer(http.HandlerFunc((&fakeAPI{}).handler))
	t.Cleanup(srv.Close)
	t.Chdir(t.TempDir())
	if err := os.WriteFile("input.txt", []byte("Portishead - Glory Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AM_DEV_TOKEN", "fake-dev")
	t.Setenv("AM_USER_TOKEN", "fake-user")

	var errOut bytes.Buffer
	code := cli(ctx, []string{"-name", "x", "input.txt"}, io.Discard, &errOut, srv.URL)
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut.String(), "Interrupted before writing") {
		t.Errorf("stderr = %q, want the interrupted message", errOut.String())
	}
}

func TestRunAppendsInsteadOfCreating(t *testing.T) {
	f, api, path := setup(t, input)
	var out bytes.Buffer

	err := run(context.Background(), options{to: importer.Targets{Playlist: true}, playlistID: "p.existing", file: path}, api, &out, discardLogger())
	if !errors.Is(err, errPartial) { // "Nobody - Nothing" has no match
		t.Fatalf("run() error = %v, want errPartial", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Errorf("created %d playlists, want 0", f.posts)
	}
	if f.appends != 1 || strings.Join(f.appended, ",") != "b1,p1" {
		t.Errorf("appends = %d with %v, want 1 with [b1 p1]", f.appends, f.appended)
	}
	if !strings.Contains(out.String(), "Added 2 songs to playlist p.existing") {
		t.Errorf("output:\n%s", out.String())
	}
}

// Ctrl-C while the create request is in flight: Apple may have applied it,
// so the error must not claim nothing changed.
func TestRunInterruptedDuringCreate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// The server only notices a dropped connection once the body
			// has been read, so read it first.
			_, _ = io.Copy(io.Discard, r.Body)
			cancel() // Ctrl-C mid-request; the client sees context.Canceled
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second): // never hang the suite
			}
			return
		}
		f.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	api := applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user")
	path := filepath.Join(t.TempDir(), "songs.txt")
	if err := os.WriteFile(path, []byte("Portishead - Glory Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run(ctx, options{to: importer.Targets{Playlist: true}, name: "Mix", file: path}, api, io.Discard, discardLogger())
	if !errors.Is(err, importer.ErrWriteInterrupted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("run() error = %v, want importer.ErrWriteInterrupted wrapping context.Canceled", err)
	}
}

func TestResolveVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }

	tests := []struct {
		name    string
		ldflags string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"release build (ldflags win)", "0.1.0", info("v0.1.0"), "0.1.0"},
		{"go install @version", "dev", info("v0.1.0"), "v0.1.0"},
		{"go build in a checkout", "dev", info("(devel)"), "dev"},
		{"no build info", "dev", noInfo, "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.ldflags, tt.read); got != tt.want {
				t.Errorf("resolveVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunLibraryAndFavorites(t *testing.T) {
	f, api, path := setup(t, input)
	var out bytes.Buffer

	opts := options{to: importer.Targets{Library: true, Favorites: true}, file: path}
	err := run(context.Background(), opts, api, &out, discardLogger())
	if !errors.Is(err, errPartial) { // "Nobody - Nothing" has no match
		t.Fatalf("run() error = %v, want errPartial", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 || f.appends != 0 {
		t.Errorf("touched a playlist: %d creates, %d appends", f.posts, f.appends)
	}
	if strings.Join(f.library, ",") != "b1,p1" {
		t.Errorf("library = %v, want [b1 p1]", f.library)
	}
	if strings.Join(f.favs, ",") != "b1,p1" {
		t.Errorf("favourites = %v, want [b1 p1]", f.favs)
	}
	for _, want := range []string{"Added 2 songs to your Library.", "Favourited 2 songs"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunAllThreeTargets(t *testing.T) {
	f, api, path := setup(t, "Portishead - Glory Box\n")
	opts := options{to: importer.Targets{Playlist: true, Library: true, Favorites: true}, name: "Mix", file: path}
	if err := run(context.Background(), opts, api, io.Discard, discardLogger()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || len(f.library) != 1 || len(f.favs) != 1 {
		t.Errorf("creates %d, library %v, favs %v; want one of each", f.posts, f.library, f.favs)
	}
}

// A failure part-way through favouriting reports how far it got, and keeps
// the error class (here auth) for the exit code.
func TestRunFavoriteFailsPartWay(t *testing.T) {
	f := &fakeAPI{}
	var favCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && favCalls.Add(1) == 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	api := applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user")
	path := filepath.Join(t.TempDir(), "songs.txt")
	if err := os.WriteFile(path, []byte("Björk - Army of Me\nPortishead - Glory Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run(context.Background(), options{to: importer.Targets{Favorites: true}, file: path}, api, io.Discard, discardLogger())
	if !errors.Is(err, applemusic.ErrUnauthorized) || !strings.Contains(err.Error(), "favourite 2 of 2") {
		t.Fatalf("run() error = %v, want ErrUnauthorized at favourite 2 of 2", err)
	}
	if code := exitCode(err); code != exitAuth {
		t.Errorf("exit code = %d, want %d", code, exitAuth)
	}
}

func TestRunWithoutTargetsRefuses(t *testing.T) {
	f, api, path := setup(t, "Portishead - Glory Box\n")
	err := run(context.Background(), options{name: "Mix", file: path}, api, io.Discard, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "no destination") {
		t.Fatalf("run() error = %v, want a no-destination error", err)
	}
	if f.posts != 0 {
		t.Errorf("sent %d POST requests, want 0", f.posts)
	}
}

// With pl,lib,fav, a failure at the library step must not suggest
// re-running pl (that would create a second playlist).
func TestRunPartialWriteSaysHowToFinish(t *testing.T) {
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/me/library" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.handler(w, r)
	}))
	t.Cleanup(srv.Close)
	api := applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user")
	path := filepath.Join(t.TempDir(), "songs.txt")
	if err := os.WriteFile(path, []byte("Portishead - Glory Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := options{to: importer.Targets{Playlist: true, Library: true, Favorites: true}, name: "Mix", storefront: "cz", file: path}
	err := run(context.Background(), opts, api, io.Discard, discardLogger())
	if err == nil {
		t.Fatal("run() error = nil, want the library failure")
	}
	// The storefront is repeated so the re-run matches the same song IDs.
	for _, want := range []string{"already done: playlist", "bootleg -to lib,fav -storefront cz " + path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || len(f.favs) != 0 {
		t.Errorf("creates %d, favs %v; want 1 create and no favourites after the failure", f.posts, f.favs)
	}
}

// Ctrl-C while waiting between favourites stops at once, and the error says
// the outcome is uncertain (earlier favourites were applied).
func TestRunCancelledBetweenFavorites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handler(w, r)
		if r.Method == http.MethodPut {
			// "Ctrl-C" shortly after the first favourite's response, i.e.
			// inside the 1s wait before the second. Cancelling in the
			// handler itself could abort the client's read of this response.
			go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		}
	}))
	t.Cleanup(srv.Close)
	api := applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user")
	path := filepath.Join(t.TempDir(), "songs.txt")
	if err := os.WriteFile(path, []byte("Björk - Army of Me\nPortishead - Glory Box\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	// -delay also paces the two searches (1s total); the wait after the first
	// favourite would be another second, but Ctrl-C cuts it short.
	err := run(ctx, options{to: importer.Targets{Favorites: true}, delay: time.Second, file: path}, api, io.Discard, discardLogger())
	if !errors.Is(err, importer.ErrWriteInterrupted) || !strings.Contains(err.Error(), "favourite 2 of 2") {
		t.Fatalf("run() error = %v, want importer.ErrWriteInterrupted at favourite 2 of 2", err)
	}
	if elapsed := time.Since(start); elapsed > 1900*time.Millisecond {
		t.Errorf("took %v: the wait after the first favourite was not cut short", elapsed)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.favs) != 1 {
		t.Errorf("favourited %v, want exactly the first song", f.favs)
	}
}

func TestRunDryRunWritesNothingAnywhere(t *testing.T) {
	f, api, path := setup(t, "Portishead - Glory Box\n")
	opts := options{to: importer.Targets{Library: true, Favorites: true}, dryRun: true, file: path}
	if err := run(context.Background(), opts, api, io.Discard, discardLogger()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts+f.appends+len(f.library)+len(f.favs) != 0 {
		t.Errorf("dry run wrote: creates %d, appends %d, library %v, favs %v", f.posts, f.appends, f.library, f.favs)
	}
}
