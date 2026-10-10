package applemusic

import (
	"context"
	"errors"
	"net/url"
)

type ratingResponse struct {
	Data []struct {
		Attributes struct {
			Value int `json:"value"`
		} `json:"attributes"`
	} `json:"data"`
}

// Unfavorite removes a song's favourite. It reads the rating first: DELETE
// answers 204 even when there was none, and a dislike (-1) is left alone.
func (c *Client) Unfavorite(ctx context.Context, songID string) (bool, error) {
	path := "/v1/me/ratings/songs/" + url.PathEscape(songID)
	var r ratingResponse
	err := c.do(ctx, "GET", path, nil, nil, &r)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(r.Data) == 0 || r.Data[0].Attributes.Value != 1 {
		return false, nil
	}
	if err := c.do(ctx, "DELETE", path, nil, nil, nil); err != nil {
		return false, err
	}
	return true, nil
}

type libraryRelation struct {
	Data []struct {
		Relationships struct {
			Library struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			} `json:"library"`
		} `json:"relationships"`
	} `json:"data"`
}

// LibraryID returns the library copy of a catalog song, if the user has one.
func (c *Client) LibraryID(ctx context.Context, storefront, songID string) (string, bool, error) {
	var resp libraryRelation
	path := "/v1/catalog/" + url.PathEscape(storefront) + "/songs/" + url.PathEscape(songID)
	if err := c.do(ctx, "GET", path, url.Values{"relate": {"library"}}, nil, &resp); err != nil {
		return "", false, err
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Relationships.Library.Data) == 0 {
		return "", false, nil
	}
	return resp.Data[0].Relationships.Library.Data[0].ID, true, nil
}

// RemoveFromLibrary deletes a library song (an i.… ID from LibraryID).
func (c *Client) RemoveFromLibrary(ctx context.Context, libraryID string) error {
	return c.do(ctx, "DELETE", "/v1/me/library/songs/"+url.PathEscape(libraryID), nil, nil, nil)
}

// PlaylistTrack is one entry of a library playlist.
type PlaylistTrack struct {
	ID        string // library song ID, what RemoveFromPlaylist takes
	CatalogID string
	Name      string
}

type playlistTracksResponse struct {
	Next string `json:"next"`
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name       string `json:"name"`
			PlayParams struct {
				CatalogID string `json:"catalogId"`
			} `json:"playParams"`
		} `json:"attributes"`
	} `json:"data"`
}

// PlaylistTracks lists a library playlist's tracks, following pagination.
func (c *Client) PlaylistTracks(ctx context.Context, playlistID string) ([]PlaylistTrack, error) {
	var out []PlaylistTrack
	path, q := "/v1/me/library/playlists/"+url.PathEscape(playlistID)+"/tracks", url.Values{"limit": {"100"}}
	for page := 0; page < maxPlaylistPages && path != ""; page++ {
		var resp playlistTracksResponse
		if err := c.do(ctx, "GET", path, q, nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			out = append(out, PlaylistTrack{ID: d.ID, CatalogID: d.Attributes.PlayParams.CatalogID, Name: d.Attributes.Name})
		}
		path, q = splitNext(resp.Next)
		if path != "" && q.Get("limit") == "" {
			q.Set("limit", "100")
		}
	}
	return out, nil
}

// RemoveFromPlaylist removes every occurrence of a track (a PlaylistTrack.ID) from a library playlist.
func (c *Client) RemoveFromPlaylist(ctx context.Context, playlistID, trackID string) error {
	q := url.Values{"ids[library-songs]": {trackID}, "mode": {"all"}}
	return c.do(ctx, "DELETE", "/v1/me/library/playlists/"+url.PathEscape(playlistID)+"/tracks", q, nil, nil)
}
