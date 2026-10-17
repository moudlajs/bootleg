package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moudlajs/bootleg/internal/applemusic"
)

// ErrPlaylistNotFound means RemoveOptions.PlaylistID isn't in the library.
var ErrPlaylistNotFound = fmt.Errorf("playlist isn't in the library: %w", applemusic.ErrNotFound)

// RemoveOptions say where Remove takes the songs from.
type RemoveOptions struct {
	From       Targets
	PlaylistID string        // with From.Playlist
	Storefront string        // to find the songs' library copies
	Delay      time.Duration // pause between requests
}

// Removed reports what Remove did, including when it stopped early.
type Removed struct {
	Favorites int      // songs unfavourited
	Playlist  int      // songs taken out of the playlist
	Library   int      // songs deleted from the library
	NotThere  []string // "<id> (not a favourite)" and the like: nothing to remove
}

// Total is how many removals were made.
func (r Removed) Total() int { return r.Favorites + r.Playlist + r.Library }

// Remove undoes Write: favourites, then playlist, then library (a favourite
// also puts the song in the library). Running it twice is harmless.
func Remove(ctx context.Context, api *applemusic.Client, opts RemoveOptions, ids []string, log *slog.Logger) (Removed, error) {
	var r Removed
	ids = unique(ids) // a repeat would be removed twice against a stale track list
	n := 0            // requests so far, for pacing
	pause := func() error {
		n++
		if n == 1 {
			return nil
		}
		return Sleep(ctx, opts.Delay)
	}

	if opts.From.Favorites {
		for _, id := range ids {
			if err := pause(); err != nil {
				return r, err
			}
			removed, err := api.Unfavorite(ctx, id)
			if err != nil {
				return r, fmt.Errorf("unfavouriting: %w", writeErr(err))
			}
			if removed {
				r.Favorites++
			} else {
				r.NotThere = append(r.NotThere, id+" (not a favourite)")
			}
		}
		log.Info("unfavourited", "tracks", r.Favorites)
	}

	if opts.From.Playlist {
		if err := pause(); err != nil {
			return r, err
		}
		tracks, err := api.PlaylistTracks(ctx, opts.PlaylistID)
		if errors.Is(err, applemusic.ErrNotFound) {
			return r, fmt.Errorf("%s: %w", opts.PlaylistID, ErrPlaylistNotFound)
		}
		if err != nil {
			return r, fmt.Errorf("reading the playlist: %w", err)
		}
		for _, id := range ids {
			found := false
			for _, tr := range tracks {
				if tr.CatalogID != id {
					continue
				}
				found = true
				if err := pause(); err != nil {
					return r, err
				}
				if err := api.RemoveFromPlaylist(ctx, opts.PlaylistID, tr.ID); err != nil {
					return r, fmt.Errorf("removing from the playlist: %w", writeErr(err))
				}
				r.Playlist++
				break // mode=all removes every occurrence of this track
			}
			if !found {
				r.NotThere = append(r.NotThere, id+" (not in the playlist)")
			}
		}
		log.Info("removed from playlist", "id", opts.PlaylistID, "tracks", r.Playlist)
	}

	if opts.From.Library {
		for _, id := range ids {
			if err := pause(); err != nil {
				return r, err
			}
			libID, ok, err := api.LibraryID(ctx, opts.Storefront, id)
			if err != nil {
				return r, fmt.Errorf("looking up the library: %w", err)
			}
			if !ok {
				r.NotThere = append(r.NotThere, id+" (not in the library)")
				continue
			}
			if err := pause(); err != nil {
				return r, err
			}
			if err := api.RemoveFromLibrary(ctx, libID); err != nil {
				return r, fmt.Errorf("removing from the library: %w", writeErr(err))
			}
			r.Library++
		}
		log.Info("removed from library", "tracks", r.Library)
	}
	return r, nil
}

func unique(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
