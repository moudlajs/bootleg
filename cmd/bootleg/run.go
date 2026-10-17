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

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/importer"
	"github.com/moudlajs/bootleg/internal/parser"
)

var errNothingMatched = errors.New("no line matched a song; nothing was changed")

var errPartial = errors.New("some lines did not match")

const unmatchedFile = "unmatched.txt"

var errNoSongs = errors.New("no songs found (only blank lines or comments?)")

func run(ctx context.Context, opts options, api *applemusic.Client, stdout io.Writer, log *slog.Logger) error {
	if opts.to == (importer.Targets{}) && !opts.dryRun {
		return errors.New("no destination: set -to (pl, lib or fav)")
	}
	queries, skipped, err := readQueries(opts.file)
	if err != nil {
		return err
	}
	log.Debug("parsed input", "file", opts.file, "queries", len(queries), "skipped", skipped)

	lines, err := importer.Resolve(ctx, api, opts.storefront, opts.delay, queries, log)
	if err != nil {
		return err
	}
	ids, unmatched := importer.Split(lines)
	closest := closestByLine(lines)

	if opts.dryRun {
		if err := printTable(stdout, lines); err != nil {
			return err
		}
		fmt.Fprintln(stdout)
		printSummary(stdout, len(ids), unmatched, skipped, "", closest)
		return partial(unmatched, "")
	}

	if opts.remove {
		return removeSongs(ctx, opts, api, stdout, log, lines, skipped)
	}

	// Write the report before touching Apple, so it exists even if a write fails.
	reportPath := writeUnmatched(opts.file, unmatched, log)
	if len(ids) == 0 {
		printSummary(stdout, 0, unmatched, skipped, reportPath, closest)
		if reportPath == "" {
			return errNothingMatched
		}
		return fmt.Errorf("%w (all lines listed in %s)", errNothingMatched, reportPath)
	}

	printSummary(stdout, len(ids), unmatched, skipped, reportPath, closest)
	w, err := importer.Write(ctx, api, importer.WriteOptions{
		To:           opts.to,
		PlaylistName: opts.name,
		PlaylistID:   opts.playlistID,
		Delay:        opts.delay,
	}, ids, log)
	printWritten(stdout, w, opts.name)
	if err != nil {
		return resumeHint(err, opts)
	}
	return partial(unmatched, reportPath)
}

// removeSongs leaves unmatched.txt alone: it belongs to the add runs.
func removeSongs(ctx context.Context, opts options, api *applemusic.Client, stdout io.Writer, log *slog.Logger,
	lines []importer.Line, skipped int) error {
	ids, unmatched := importer.Split(lines)
	printSummary(stdout, len(ids), unmatched, skipped, "", closestByLine(lines))
	if len(ids) == 0 {
		return errNothingMatched
	}
	r, err := importer.Remove(ctx, api, importer.RemoveOptions{
		From:       opts.to,
		PlaylistID: opts.playlistID,
		Storefront: opts.storefront,
		Delay:      opts.delay,
	}, ids, log)
	printRemoved(stdout, r, opts.playlistID, songNames(lines))
	if err != nil {
		if r.Total() > 0 {
			return fmt.Errorf("%w (already removed: %d; running the same command again is safe)", err, r.Total())
		}
		return err
	}
	return partial(unmatched, "")
}

// songNames maps a matched catalog ID to "Artist - Title".
func songNames(lines []importer.Line) map[string]string {
	m := map[string]string{}
	for _, l := range lines {
		if l.OK {
			m[l.Song.ID] = l.Song.Artist + " - " + l.Song.Name
		}
	}
	return m
}

func printRemoved(out io.Writer, r importer.Removed, playlistID string, names map[string]string) {
	if r.Favorites > 0 {
		fmt.Fprintf(out, "Unfavourited %d songs.\n", r.Favorites)
	}
	if r.Playlist > 0 {
		fmt.Fprintf(out, "Removed %d songs from playlist %s.\n", r.Playlist, playlistID)
	}
	if r.Library > 0 {
		fmt.Fprintf(out, "Removed %d songs from your Library.\n", r.Library)
	}
	if len(r.NotThere) > 0 {
		fmt.Fprintln(out, "Nothing to remove for:")
		for _, s := range r.NotThere {
			// Entries are "<id> (<reason>)"; show the song instead of the ID.
			if id, reason, ok := strings.Cut(s, " "); ok && names[id] != "" {
				s = names[id] + " " + reason
			}
			fmt.Fprintf(out, "  %s\n", s)
		}
	}
}

func printWritten(out io.Writer, w importer.Written, name string) {
	switch {
	case w.PlaylistCreated:
		fmt.Fprintf(out, "Created %q with %d songs (playlist ID %s).\n", name, w.Playlist, w.PlaylistID)
	case w.Playlist > 0:
		fmt.Fprintf(out, "Added %d songs to playlist %s.\n", w.Playlist, w.PlaylistID)
	}
	if w.Library > 0 {
		fmt.Fprintf(out, "Added %d songs to your Library.\n", w.Library)
	}
	if w.Favorites > 0 {
		fmt.Fprintf(out, "Favourited %d songs (Favourite Songs and Library). Your devices may take a few minutes to show them.\n", w.Favorites)
	}
}

// resumeHint repeats -storefront so the re-run matches the same song IDs.
func resumeHint(err error, opts options) error {
	var inc *importer.IncompleteError
	if !errors.As(err, &inc) {
		return err
	}
	cmd := "bootleg -to " + strings.Join(inc.Rest, ",")
	if opts.storefront != "" {
		cmd += " -storefront " + opts.storefront
	}
	return fmt.Errorf("%w (already done: %s; to finish, run: %s %s)",
		err, strings.Join(inc.Done, ", "), cmd, opts.file)
}

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

// closestByLine maps an unmatched line's number to its best alternative.
func closestByLine(lines []importer.Line) map[int]string {
	m := map[int]string{}
	for _, l := range lines {
		if !l.OK && len(l.Alternatives) > 0 {
			a := l.Alternatives[0]
			m[l.Query.Line] = a.Artist + " - " + a.Name
		}
	}
	return m
}

func printSummary(w io.Writer, matched int, unmatched []parser.Query, skipped int, reportPath string, closest map[int]string) {
	fmt.Fprintf(w, "Matched %d, unmatched %d, skipped %d (blank or comment).\n", matched, len(unmatched), skipped)
	if len(unmatched) == 0 {
		return
	}
	fmt.Fprintln(w, "Unmatched:")
	for _, q := range unmatched {
		if c, ok := closest[q.Line]; ok {
			fmt.Fprintf(w, "  line %d: %s (closest: %s)\n", q.Line, q.Raw, c)
		} else {
			fmt.Fprintf(w, "  line %d: %s\n", q.Line, q.Raw)
		}
	}
	if reportPath != "" {
		fmt.Fprintf(w, "Written to %s - fix the lines and feed it back in.\n", reportPath)
	}
}

// writeUnmatched never fails the run: problems are only logged, since the summary has the same lines.
func writeUnmatched(input string, unmatched []parser.Query, log *slog.Logger) string {
	path := filepath.Join(filepath.Dir(input), unmatchedFile)

	// The input may itself be a re-fed unmatched.txt; never overwrite it.
	if sameFile(path, input) {
		if len(unmatched) > 0 {
			log.Warn("not overwriting the input file with the report; the unmatched lines are in the summary", "path", path)
		}
		return ""
	}

	if len(unmatched) == 0 {
		// Remove a stale report from an earlier run.
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

func sameFile(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

func readQueries(path string) ([]parser.Query, int, error) {
	f, err := os.Open(path) // #nosec G304 -- the user names the file to read.
	if err != nil {
		return nil, 0, fmt.Errorf("open input: %w", err)
	}
	defer func() { _ = f.Close() }()

	queries, skipped, err := parser.Parse(f)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}
	if len(queries) == 0 {
		return nil, 0, fmt.Errorf("%s: %w", path, errNoSongs)
	}
	return queries, skipped, nil
}

func printTable(w io.Writer, lines []importer.Line) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LINE\tQUERY\tMATCH\tID")
	for _, l := range lines {
		match, id := "(no match)", "-"
		if l.OK {
			match, id = l.Song.Artist+" - "+l.Song.Name, l.Song.ID
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", l.Query.Line, l.Query.Raw, match, id)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}
