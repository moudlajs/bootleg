package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

	r, err := importer.Remove(ctx, s.API, importer.RemoveOptions{
		From: t, PlaylistID: pl, Storefront: s.Storefront, Delay: s.Delay,
	}, ids, s.Log)
	out.Unfavourited, out.RemovedFromPlaylist, out.RemovedFromLibrary = r.Favorites, r.Playlist, r.Library
	out.NotThere = append(out.NotThere, r.NotThere...)
	if err == nil {
		return out, nil
	}
	msg := people(err)
	if errors.Is(err, importer.ErrPlaylistNotFound) {
		msg = fmt.Sprintf("playlist %s isn't in the library; use list_playlists to find the right playlist_id", pl)
	}
	if r.Total() > 0 {
		msg += fmt.Sprintf(". Already removed: %d; calling remove_songs again with the same songs is safe", r.Total())
	}
	return out, errors.New(msg)
}

func splitForRemove(lines []importer.Line) ([]string, []Unmatched) {
	ids, unmatched := importer.Split(lines)
	out := make([]Unmatched, 0, len(unmatched))
	for _, q := range unmatched {
		out = append(out, Unmatched{Line: q.Line, Query: q.Raw})
	}
	return ids, out
}
