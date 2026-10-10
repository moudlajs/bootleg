// Package importer matches song queries against the catalog and writes the
// matches to a playlist, the library and/or Favourite Songs; it never prints.
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

// ErrWriteInterrupted means a write was cancelled in flight, so Apple may have applied it.
var ErrWriteInterrupted = errors.New("interrupted while writing; check your library, the change may have been applied")

// Targets are the places matched songs go.
type Targets struct {
	Playlist  bool // create (PlaylistName) or append to (PlaylistID) a playlist
	Library   bool // the library ("Songs")
	Favorites bool // the star, i.e. the automatic Favourite Songs playlist
}

// ParseTargets reads a comma-separated list of pl, lib and fav (or long forms).
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

// Line is one query and the song chosen for it, if any.
type Line struct {
	Query parser.Query
	Song  applemusic.Song
	OK    bool
	// Alternatives are the closest acceptable results when OK is false.
	Alternatives []applemusic.Song
}

// maxAlternatives caps the suggestions offered for an unmatched line.
const maxAlternatives = 3

// Resolve searches sequentially, pausing delay between queries so traffic looks like a person.
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
		l := Line{Query: q}
		for j, c := range candidates {
			if len(l.Alternatives) < maxAlternatives && matcher.Acceptable(q.Artist, q.Title, c) {
				l.Alternatives = append(l.Alternatives, songs[j])
			}
		}
		return l, nil
	}
	return Line{Query: q, Song: songs[i], OK: true}, nil
}

// Split returns the matched song IDs, in input order, and the unmatched queries.
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

// IncompleteError means Write stopped after at least one destination was done.
type IncompleteError struct {
	Done []string // completed: "playlist", "library"
	Rest []string // still to do, as -to values: "lib", "fav"
	Err  error
}

func (e *IncompleteError) Error() string { return e.Err.Error() }

// Unwrap keeps the cause visible to errors.Is (auth, rate limit, cancel).
func (e *IncompleteError) Unwrap() error { return e.Err }

// Write sends ids to playlist, library, then favourites, stopping at the first error.
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

func writeErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrWriteInterrupted, err)
	}
	return err
}

// Sleep waits for d, or returns ctx's error early if ctx is cancelled.
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
