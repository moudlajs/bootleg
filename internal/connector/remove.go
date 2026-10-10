package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/importer"
)

// RemoveInput is remove_songs' arguments.
type RemoveInput struct {
	Songs      string   `json:"songs,omitempty" jsonschema:"Artist - Title lines, as for preview_songs"`
	SongIDs    []string `json:"song_ids,omitempty" jsonschema:"catalog IDs, e.g. from preview_songs"`
	From       []string `json:"from" jsonschema:"one or more of: pl (a playlist, needs playlist_id), lib (the Library), fav (Favourite Songs)"`
	PlaylistID string   `json:"playlist_id,omitempty" jsonschema:"with pl: the playlist to remove from (ID from list_playlists)"`
}

// RemoveOutput is remove_songs' result.
type RemoveOutput struct {
	Unfavourited        int         `json:"unfavourited"`
	RemovedFromPlaylist int         `json:"removed_from_playlist"`
	RemovedFromLibrary  int         `json:"removed_from_library"`
	NotThere            []string    `json:"not_there" jsonschema:"song IDs and where they weren't (nothing to remove)"`
	Unmatched           []Unmatched `json:"unmatched" jsonschema:"lines that matched no catalog song, so nothing was removed for them"`
}

func (s *Service) remove(ctx context.Context, in RemoveInput) (RemoveOutput, error) {
	if len(in.From) == 0 {
		return RemoveOutput{}, errors.New("say where to remove from: from = pl, lib and/or fav")
	}
	t, err := importer.ParseTargets(strings.Join(in.From, ","))
	if err != nil {
		return RemoveOutput{}, err
	}
	pl := strings.TrimSpace(in.PlaylistID)
	switch {
	case t.Playlist && pl == "":
		return RemoveOutput{}, errors.New("from pl needs playlist_id (see list_playlists)")
	case !t.Playlist && pl != "":
		return RemoveOutput{}, errors.New("playlist_id only applies with pl in from")
	}

	byID, err := songIDs(in.SongIDs)
	if err != nil {
		return RemoveOutput{}, err
	}
	out := RemoveOutput{NotThere: []string{}, Unmatched: []Unmatched{}}
	var ids []string
	if strings.TrimSpace(in.Songs) != "" {
		lines, _, err := s.resolve(ctx, in.Songs)
		if err != nil {
			return RemoveOutput{}, err
		}
		var unmatched []Unmatched
		ids, unmatched = splitForRemove(lines)
		out.Unmatched = unmatched
	}
	ids = appendNew(ids, byID)
	switch {
	case len(ids) == 0 && len(out.Unmatched) > 0:
		return out, errors.New("none of the songs matched a catalog song, so nothing was removed")
	case len(ids) == 0:
		return out, errors.New("no songs given: send songs (Artist - Title lines) and/or song_ids")
	case len(ids) > maxSongs:
		return out, fmt.Errorf("%d songs is more than %d per call; split the list", len(ids), maxSongs)
	}

	// A favourite also puts the song in the library, so unfavourite first.
	if t.Favorites {
		if err := s.unfavourite(ctx, ids, &out); err != nil {
			return out, err
		}
	}
	if t.Playlist {
		if err := s.removeFromPlaylist(ctx, pl, ids, &out); err != nil {
			return out, err
		}
	}
	if t.Library {
		if err := s.removeFromLibrary(ctx, ids, &out); err != nil {
			return out, err
		}
	}
	return out, nil
}

func splitForRemove(lines []importer.Line) ([]string, []Unmatched) {
	ids, unmatched := importer.Split(lines)
	out := make([]Unmatched, 0, len(unmatched))
	for _, q := range unmatched {
		out = append(out, Unmatched{Line: q.Line, Query: q.Raw})
	}
	return ids, out
}

func (s *Service) unfavourite(ctx context.Context, ids []string, out *RemoveOutput) error {
	for i, id := range ids {
		if err := s.pause(ctx, i); err != nil {
			return err
		}
		removed, err := s.API.Unfavorite(ctx, id)
		if err != nil {
			return s.partial("unfavouriting", out, err)
		}
		if removed {
			out.Unfavourited++
		} else {
			out.NotThere = append(out.NotThere, id+" (not a favourite)")
		}
	}
	return nil
}

func (s *Service) removeFromPlaylist(ctx context.Context, playlistID string, ids []string, out *RemoveOutput) error {
	tracks, err := s.API.PlaylistTracks(ctx, playlistID)
	if errors.Is(err, applemusic.ErrNotFound) {
		return fmt.Errorf("playlist %s isn't in the library; use list_playlists to find the right playlist_id", playlistID)
	}
	if err != nil {
		return s.partial("reading the playlist", out, err)
	}
	n := 0
	for _, id := range ids {
		found := false
		for _, tr := range tracks {
			if tr.CatalogID != id {
				continue
			}
			found = true
			if err := s.pause(ctx, n); err != nil {
				return err
			}
			n++
			if err := s.API.RemoveFromPlaylist(ctx, playlistID, tr.ID); err != nil {
				return s.partial("removing from the playlist", out, err)
			}
			out.RemovedFromPlaylist++
			break // mode=all removes every occurrence of this track
		}
		if !found {
			out.NotThere = append(out.NotThere, id+" (not in the playlist)")
		}
	}
	return nil
}

func (s *Service) removeFromLibrary(ctx context.Context, ids []string, out *RemoveOutput) error {
	for i, id := range ids {
		if err := s.pause(ctx, i); err != nil {
			return err
		}
		libID, ok, err := s.API.LibraryID(ctx, s.Storefront, id)
		if err != nil {
			return s.partial("looking up the library", out, err)
		}
		if !ok {
			out.NotThere = append(out.NotThere, id+" (not in the library)")
			continue
		}
		if err := s.API.RemoveFromLibrary(ctx, libID); err != nil {
			return s.partial("removing from the library", out, err)
		}
		out.RemovedFromLibrary++
	}
	return nil
}

func (s *Service) pause(ctx context.Context, i int) error {
	if i == 0 {
		return nil
	}
	return importer.Sleep(ctx, s.Delay)
}

// partial words an error that may come after some songs were already removed.
func (s *Service) partial(step string, out *RemoveOutput, err error) error {
	done := out.Unfavourited + out.RemovedFromPlaylist + out.RemovedFromLibrary
	if done == 0 {
		return fmt.Errorf("%s: %s", step, people(err))
	}
	return fmt.Errorf("%s: %s. Already removed: %d; calling remove_songs again with the same songs is safe", step, people(err), done)
}
