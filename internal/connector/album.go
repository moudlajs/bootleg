package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/matcher"
)

// AlbumInput is album_tracks' arguments.
type AlbumInput struct {
	Album   string `json:"album,omitempty" jsonschema:"the album as Artist - Album, e.g. Sigur Rós - Takk"`
	AlbumID string `json:"album_id,omitempty" jsonschema:"a catalog album ID instead, e.g. one of other_editions"`
}

// AlbumInfo describes a catalog album.
type AlbumInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Artist      string `json:"artist"`
	Tracks      int    `json:"tracks,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
}

// AlbumOutput is album_tracks' result.
type AlbumOutput struct {
	Album         *AlbumInfo  `json:"album,omitempty" jsonschema:"the album found; empty when none matched"`
	Tracks        []Match     `json:"tracks" jsonschema:"in album order; add them with add_songs song_ids"`
	OtherEditions []AlbumInfo `json:"other_editions" jsonschema:"other search results, e.g. deluxe editions; offer them if they fit better"`
	Note          string      `json:"note"`
}

const albumNote = "Show the tracklist and where it will go; after the user confirms, add the tracks with add_songs " +
	"song_ids in this order (at most 50 per call)."

func (s *Service) album(ctx context.Context, in AlbumInput) (AlbumOutput, error) {
	out := AlbumOutput{Tracks: []Match{}, OtherEditions: []AlbumInfo{}, Note: albumNote}
	id := strings.TrimSpace(in.AlbumID)
	name := strings.TrimSpace(in.Album)
	switch {
	case id != "" && name != "":
		return out, errors.New("give either album or album_id, not both")
	case id != "":
		ids, err := songIDs([]string{id})
		if err != nil {
			return out, errors.New("album_id must be a catalog album ID (digits)")
		}
		out.Album = &AlbumInfo{ID: ids[0]}
	case name != "":
		albums, err := s.API.SearchAlbums(ctx, s.Storefront, strings.Replace(name, " - ", " ", 1))
		if err != nil {
			return out, errors.New(people(err))
		}
		best, ok := bestAlbum(name, albums)
		for i, a := range albums {
			if !ok || i != best {
				out.OtherEditions = append(out.OtherEditions, albumInfo(a))
			}
		}
		if !ok {
			out.Note = "No album matched; if one of other_editions is it, call album_tracks again with its album_id."
			return out, nil
		}
		info := albumInfo(albums[best])
		out.Album = &info
	default:
		return out, errors.New("say which album: album as Artist - Album, or album_id")
	}

	tracks, err := s.API.AlbumTracks(ctx, s.Storefront, out.Album.ID)
	if errors.Is(err, applemusic.ErrNotFound) {
		return out, fmt.Errorf("album %s isn't in the catalog", out.Album.ID)
	}
	if err != nil {
		return out, errors.New(people(err))
	}
	for i, t := range tracks {
		out.Tracks = append(out.Tracks, Match{Line: i + 1, Query: t.Album, Artist: t.Artist, Title: t.Name, ID: t.ID})
		if out.Album.Name == "" {
			out.Album.Name, out.Album.Artist = t.Album, t.Artist
		}
	}
	out.Album.Tracks = len(tracks)
	return out, nil
}

// bestAlbum picks the result for "Artist - Album" the way songs are matched,
// with the album name in the title's place.
func bestAlbum(query string, albums []applemusic.Album) (int, bool) {
	artist, title, ok := strings.Cut(query, " - ")
	if !ok {
		artist, title = "", query
	}
	candidates := make([]matcher.Candidate, len(albums))
	for i, a := range albums {
		candidates[i] = matcher.Candidate{Artist: a.Artist, Title: a.Name}
	}
	return matcher.Best(strings.TrimSpace(artist), strings.TrimSpace(title), candidates)
}

func albumInfo(a applemusic.Album) AlbumInfo {
	return AlbumInfo{ID: a.ID, Name: a.Name, Artist: a.Artist, Tracks: a.TrackCount, ReleaseDate: a.ReleaseDate}
}
