// Package importer is bootleg's engine: it searches the catalog for song
// queries, picks the best match for each, and writes the matches to a
// playlist, the library and/or Favourite Songs. The CLI and the connector
// both use it; it never prints and never touches files.
package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/matcher"
	"github.com/moudlajs/bootleg/internal/parser"
)

// ErrWriteInterrupted means the context was cancelled while a write was in
// flight, so Apple may or may not have applied it.
var ErrWriteInterrupted = errors.New("interrupted while writing; check your library, the change may have been applied")

// Targets are the places matched songs go.
type Targets struct {
	Playlist  bool // create (PlaylistName) or append to (PlaylistID) a playlist
	Library   bool // the library ("Songs")
	Favorites bool // the star, i.e. the automatic Favourite Songs playlist
}

// ParseTargets reads a comma-separated list of pl, lib and fav, or their
// long forms. Order, case and repeats don't matter.
func ParseTargets(s string) (Targets, error) {
	var t Targets
	for _, part := range strings.Split(s, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "pl", "playlist":
			t.Playlist = true
		case "lib", "library":
			t.Library = true
		case "fav", "favs", "favorites", "favourites":
			t.Favorites = true
		default:
			return t, fmt.Errorf("unknown destination %q (use pl, lib, fav, or a comma list like lib,fav)", strings.TrimSpace(part))
		}
	}
	return t, nil
}

// Line is one query and the song chosen for it. OK is false when no search
// result was acceptable.
type Line struct {
	Query parser.Query
	Song  applemusic.Song
	OK    bool
}

// Resolve searches for each query in order and picks the best result. It is
// sequential on purpose, pausing delay between searches so the traffic looks
// like a person using the web player, not a scraper.
func Resolve(ctx context.Context, api *applemusic.Client, storefront string, delay time.Duration, queries []parser.Query, log *slog.Logger) ([]Line, error) {
	lines := make([]Line, 0, len(queries))
	for i, q := range queries {
		if i > 0 {
			if err := Sleep(ctx, delay); err != nil {
				return nil, fmt.Errorf("line %d: %w", q.Line, err)
			}
		}
		l, err := resolve(ctx, api, storefront, q)
		if err != nil {
			return nil, err
		}
		if l.OK {
			log.Debug("matched", "line", q.Line, "query", q.Raw, "artist", l.Song.Artist, "title", l.Song.Name, "id", l.Song.ID)
		} else {
			log.Warn("no match", "line", q.Line, "query", q.Raw)
		}
		lines = append(lines, l)
	}
	return lines, nil
}

func resolve(ctx context.Context, api *applemusic.Client, storefront string, q parser.Query) (Line, error) {
	songs, err := api.Search(ctx, storefront, q.Term())
	if err != nil {
		return Line{}, fmt.Errorf("line %d: search: %w", q.Line, err)
	}
	candidates := make([]matcher.Candidate, len(songs))
	for i, s := range songs {
		candidates[i] = matcher.Candidate{Artist: s.Artist, Title: s.Name}
	}
	i, ok := matcher.Best(q.Artist, q.Title, candidates)
	if !ok {
		return Line{Query: q}, nil
	}
	return Line{Query: q, Song: songs[i], OK: true}, nil
}

// Split returns the matched song IDs, in input order, and the unmatched
// queries.
func Split(lines []Line) (ids []string, unmatched []parser.Query) {
	for _, l := range lines {
		if l.OK {
			ids = append(ids, l.Song.ID)
		} else {
			unmatched = append(unmatched, l.Query)
		}
	}
	return ids, unmatched
}

// WriteOptions say where Write sends the songs.
type WriteOptions struct {
	To           Targets
	PlaylistName string        // create a playlist with this name, or
	PlaylistID   string        // append to this existing one
	Delay        time.Duration // pause between favourites
}

// Written reports what Write completed, including when it stopped early.
type Written struct {
	PlaylistID      string // the playlist written to, if any
	PlaylistCreated bool   // true if Write created it
	Playlist        int    // songs added to the playlist
	Library         int    // songs added to the library
	Favorites       int    // songs favourited
}

// IncompleteError means Write stopped after at least one destination was
// done. Done and Rest name destinations ("playlist", "lib", "fav") so the
// caller can say how to finish without repeating the playlist step.
type IncompleteError struct {
	Done []string // completed: "playlist", "library"
	Rest []string // still to do, as -to values: "lib", "fav"
	Err  error
}

func (e *IncompleteError) Error() string { return e.Err.Error() }

// Unwrap keeps the cause visible to errors.Is (auth, rate limit, cancel).
func (e *IncompleteError) Unwrap() error { return e.Err }

// Write sends ids to each destination in opts.To, in a fixed order:
// playlist, library, favourites. It stops at the first error. Favourites
// are idempotent, so repeating that step is safe; repeating the playlist
// step would create a second playlist, which IncompleteError helps avoid.
func Write(ctx context.Context, api *applemusic.Client, opts WriteOptions, ids []string, log *slog.Logger) (Written, error) {
	var w Written
	var done, rest []string
	if opts.To.Library {
		rest = append(rest, "lib")
	}
	if opts.To.Favorites {
		rest = append(rest, "fav")
	}
	fail := func(err error) (Written, error) {
		err = writeErr(err)
		if len(done) == 0 {
			return w, err
		}
		return w, &IncompleteError{Done: done, Rest: rest, Err: err}
	}

	if opts.To.Playlist {
		if opts.PlaylistID != "" {
			if err := api.AddTracks(ctx, opts.PlaylistID, ids); err != nil {
				return fail(fmt.Errorf("add to playlist %s: %w", opts.PlaylistID, err))
			}
			w.PlaylistID = opts.PlaylistID
			log.Info("added to playlist", "id", opts.PlaylistID, "tracks", len(ids))
		} else {
			id, err := api.CreatePlaylist(ctx, opts.PlaylistName, ids)
			if err != nil {
				return fail(fmt.Errorf("create playlist %q: %w", opts.PlaylistName, err))
			}
			w.PlaylistID, w.PlaylistCreated = id, true
			log.Info("created playlist", "name", opts.PlaylistName, "id", id, "tracks", len(ids))
		}
		w.Playlist = len(ids)
		done = append(done, "playlist")
	}

	if opts.To.Library {
		if err := api.AddToLibrary(ctx, ids); err != nil {
			return fail(fmt.Errorf("add to library: %w", err))
		}
		w.Library = len(ids)
		log.Info("added to library", "tracks", len(ids))
		done = append(done, "library")
		rest = rest[1:]
	}

	if opts.To.Favorites {
		for i, id := range ids {
			if i > 0 {
				if err := Sleep(ctx, opts.Delay); err != nil {
					return fail(fmt.Errorf("favourite %d of %d: %w", i+1, len(ids), err))
				}
			}
			if err := api.Favorite(ctx, id); err != nil {
				return fail(fmt.Errorf("favourite %d of %d: %w", i+1, len(ids), err))
			}
			w.Favorites++
			log.Debug("favourited", "id", id)
		}
		log.Info("favourited", "tracks", len(ids))
	}
	return w, nil
}

// writeErr marks a cancellation during a write, where the outcome on
// Apple's side is unknown, so it isn't reported as "nothing was changed".
func writeErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrWriteInterrupted, err)
	}
	return err
}

// Sleep waits for d, or returns early with ctx's error if ctx is cancelled
// first. time.Sleep can't be interrupted, so it would delay Ctrl-C.
func Sleep(ctx context.Context, d time.Duration) error {
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
