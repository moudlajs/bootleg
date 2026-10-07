package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/matcher"
	"github.com/moudlajs/bootleg/internal/parser"
)

// errNothingMatched means no line produced a usable match, so there is
// nothing to put in a playlist.
var errNothingMatched = errors.New("no line matched a song; nothing to import")

// errPartial means the run finished but some lines had no match; they are
// listed in unmatched.txt. It maps to exit code 1.
var errPartial = errors.New("some lines did not match")

// unmatchedFile is written next to the input file.
const unmatchedFile = "unmatched.txt"

// errWriteInterrupted means the run was cancelled while the create or
// append request was in flight, so Apple may or may not have applied it.
var errWriteInterrupted = errors.New("interrupted while writing the playlist; check your library, the change may have been applied")

// errNoSongs means the input file had no song lines at all.
var errNoSongs = errors.New("no songs found (only blank lines or comments?)")

// resolved pairs an input line with its chosen song. ok is false when no
// search result was acceptable.
type resolved struct {
	query parser.Query
	song  applemusic.Song
	ok    bool
}

// run does the actual work: read, search, match, then print or create.
func run(ctx context.Context, opts options, api *applemusic.Client, stdout io.Writer, log *slog.Logger) error {
	// parseFlags always sets at least one; this catches a caller that forgot.
	if opts.to == (targets{}) && !opts.dryRun {
		return errors.New("no destination: set -to (pl, lib or fav)")
	}
	queries, skipped, err := readQueries(opts.file)
	if err != nil {
		return err
	}
	log.Debug("parsed input", "file", opts.file, "queries", len(queries), "skipped", skipped)

	results := make([]resolved, 0, len(queries))
	for i, q := range queries {
		// Sequential on purpose, with a pause between searches so we look
		// like a person using the web player, not a scraper.
		if i > 0 {
			if err := sleep(ctx, opts.delay); err != nil {
				return fmt.Errorf("line %d: %w", q.Line, err)
			}
		}
		r, err := resolve(ctx, api, opts.storefront, q)
		if err != nil {
			return err
		}
		if r.ok {
			log.Debug("matched", "line", q.Line, "query", q.Raw, "artist", r.song.Artist, "title", r.song.Name, "id", r.song.ID)
		} else {
			log.Warn("no match", "line", q.Line, "query", q.Raw)
		}
		results = append(results, r)
	}

	var ids []string
	var unmatched []parser.Query
	for _, r := range results {
		if r.ok {
			ids = append(ids, r.song.ID)
		} else {
			unmatched = append(unmatched, r.query)
		}
	}

	if opts.dryRun {
		if err := printTable(stdout, results); err != nil {
			return err
		}
		fmt.Fprintln(stdout)
		printSummary(stdout, len(ids), unmatched, skipped, "")
		return partial(unmatched, "")
	}

	// Write the report before creating the playlist, so it exists even if
	// the create call fails, or nothing matched at all, and the user wants
	// to fix lines and retry.
	reportPath := writeUnmatched(opts.file, unmatched, log)
	if len(ids) == 0 {
		printSummary(stdout, 0, unmatched, skipped, reportPath)
		if reportPath == "" {
			return errNothingMatched
		}
		return fmt.Errorf("%w (all lines listed in %s)", errNothingMatched, reportPath)
	}

	// Summary first, so the user sees what matched even if a write fails.
	printSummary(stdout, len(ids), unmatched, skipped, reportPath)
	if err := writeTargets(ctx, opts, api, ids, stdout, log); err != nil {
		return err
	}
	return partial(unmatched, reportPath)
}

// writeTargets sends the matched songs to each destination in -to, in a
// fixed order: playlist, library, favourites. It stops at the first error,
// and that error says what was already done and how to finish without
// repeating it (re-running with pl would create a second playlist).
func writeTargets(ctx context.Context, opts options, api *applemusic.Client, ids []string, stdout io.Writer, log *slog.Logger) error {
	var done []string // human-readable, for the error
	var rest []string // -to values left after the playlist step
	if opts.to.library {
		rest = append(rest, "lib")
	}
	if opts.to.favorites {
		rest = append(rest, "fav")
	}
	// fail wraps err (keeping its class for the exit code) with a resume hint.
	fail := func(err error) error {
		err = writeErr(err)
		if len(done) == 0 {
			return err
		}
		// Favourites are idempotent, so re-running the fav step from the
		// start is safe; the playlist step is never repeated.
		return fmt.Errorf("%w (already done: %s; to finish, run: bootleg -to %s %s)",
			err, strings.Join(done, ", "), strings.Join(rest, ","), opts.file)
	}

	if opts.to.playlist {
		if opts.playlistID != "" {
			if err := api.AddTracks(ctx, opts.playlistID, ids); err != nil {
				return fail(fmt.Errorf("add to playlist %s: %w", opts.playlistID, err))
			}
			log.Info("added to playlist", "id", opts.playlistID, "tracks", len(ids))
			fmt.Fprintf(stdout, "Added %d songs to playlist %s.\n", len(ids), opts.playlistID)
		} else {
			id, err := api.CreatePlaylist(ctx, opts.name, ids)
			if err != nil {
				return fail(fmt.Errorf("create playlist %q: %w", opts.name, err))
			}
			log.Info("created playlist", "name", opts.name, "id", id, "tracks", len(ids))
			fmt.Fprintf(stdout, "Created %q with %d songs (playlist ID %s).\n", opts.name, len(ids), id)
		}
		done = append(done, "playlist")
	}

	if opts.to.library {
		if err := api.AddToLibrary(ctx, ids); err != nil {
			return fail(fmt.Errorf("add to library: %w", err))
		}
		log.Info("added to library", "tracks", len(ids))
		fmt.Fprintf(stdout, "Added %d songs to your Library.\n", len(ids))
		done = append(done, "library")
		rest = rest[1:]
	}

	if opts.to.favorites {
		// One request per song, paced like the searches.
		for i, id := range ids {
			if i > 0 {
				if err := sleep(ctx, opts.delay); err != nil {
					return fail(fmt.Errorf("favourite %d of %d: %w", i+1, len(ids), err))
				}
			}
			if err := api.Favorite(ctx, id); err != nil {
				return fail(fmt.Errorf("favourite %d of %d: %w", i+1, len(ids), err))
			}
			log.Debug("favourited", "id", id)
		}
		log.Info("favourited", "tracks", len(ids))
		fmt.Fprintf(stdout, "Favourited %d songs (Favourite Songs and Library).\n", len(ids))
	}
	return nil
}

// writeErr marks a cancellation during the create/append request, where
// the outcome on Apple's side is unknown, so it isn't reported as "nothing
// was changed".
func writeErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", errWriteInterrupted, err)
	}
	return err
}

// partial returns errPartial, with the count and report location, when
// any line went unmatched.
func partial(unmatched []parser.Query, reportPath string) error {
	switch {
	case len(unmatched) == 0:
		return nil
	case reportPath == "":
		return fmt.Errorf("%d line(s) unmatched: %w", len(unmatched), errPartial)
	default:
		return fmt.Errorf("%d line(s) unmatched, listed in %s: %w", len(unmatched), reportPath, errPartial)
	}
}

func printSummary(w io.Writer, matched int, unmatched []parser.Query, skipped int, reportPath string) {
	fmt.Fprintf(w, "Matched %d, unmatched %d, skipped %d (blank or comment).\n", matched, len(unmatched), skipped)
	if len(unmatched) == 0 {
		return
	}
	fmt.Fprintln(w, "Unmatched:")
	for _, q := range unmatched {
		fmt.Fprintf(w, "  line %d: %s\n", q.Line, q.Raw)
	}
	if reportPath != "" {
		fmt.Fprintf(w, "Written to %s - fix the lines and feed it back in.\n", reportPath)
	}
}

// writeUnmatched writes the unmatched lines verbatim to unmatched.txt next
// to the input, so the file can be edited and used as input again. With
// nothing unmatched it removes a stale report from an earlier run, unless
// that report is the input itself. It returns the path written, or "".
//
// It never fails the run: the report is a convenience (the summary prints
// the same lines), so problems are logged as warnings and the playlist is
// still created.
func writeUnmatched(input string, unmatched []parser.Query, log *slog.Logger) string {
	path := filepath.Join(filepath.Dir(input), unmatchedFile)

	// Never touch the input file: when the input is itself unmatched.txt
	// (a report being re-fed), leave it exactly as the user wrote it.
	if sameFile(path, input) {
		if len(unmatched) > 0 {
			log.Warn("not overwriting the input file with the report; the unmatched lines are in the summary", "path", path)
		}
		return ""
	}

	if len(unmatched) == 0 {
		// A missing file is the normal case. Any other failure only leaves
		// a leftover file behind, which must not cost the user the playlist.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("could not remove stale report", "path", path, "error", err)
		}
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# bootleg: no match found for these lines of %s.\n", filepath.Base(input))
	b.WriteString("# Fix them (e.g. \"Artist - Title\") and run bootleg on this file.\n")
	for _, q := range unmatched {
		b.WriteString(q.Raw + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		log.Warn("could not write unmatched report; the lines are in the summary below", "path", path, "error", err)
		return ""
	}
	return path
}

// sameFile reports whether a and b name the same existing file.
func sameFile(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

// sleep waits for d, or returns early with ctx's error if ctx is cancelled
// first. time.Sleep can't be interrupted, so it would delay Ctrl-C.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func readQueries(path string) ([]parser.Query, int, error) {
	f, err := os.Open(path) // #nosec G304 -- the user names the file to read.
	if err != nil {
		return nil, 0, fmt.Errorf("open input: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only; a close error can't lose data

	queries, skipped, err := parser.Parse(f)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	if len(queries) == 0 {
		return nil, 0, fmt.Errorf("%s: %w", path, errNoSongs)
	}
	return queries, skipped, nil
}

// resolve searches for one query and picks the best result.
func resolve(ctx context.Context, api *applemusic.Client, storefront string, q parser.Query) (resolved, error) {
	songs, err := api.Search(ctx, storefront, q.Term())
	if err != nil {
		return resolved{}, fmt.Errorf("line %d: search: %w", q.Line, err)
	}

	candidates := make([]matcher.Candidate, len(songs))
	for i, s := range songs {
		candidates[i] = matcher.Candidate{Artist: s.Artist, Title: s.Name}
	}
	i, ok := matcher.Best(q.Artist, q.Title, candidates)
	if !ok {
		return resolved{query: q}, nil
	}
	return resolved{query: q, song: songs[i], ok: true}, nil
}

// printTable writes the dry-run report as aligned columns.
func printTable(w io.Writer, results []resolved) error {
	// tabwriter pads tab-separated cells into columns; nothing is written
	// until Flush.
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LINE\tQUERY\tMATCH\tID")
	for _, r := range results {
		match, id := "(no match)", "-"
		if r.ok {
			match, id = r.song.Artist+" - "+r.song.Name, r.song.ID
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", r.query.Line, r.query.Raw, match, id)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}
