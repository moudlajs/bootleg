package applemusic

import (
	"context"
	"errors"
	"net/url"
)

type trackRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type createPlaylistRequest struct {
	Attributes struct {
		Name string `json:"name"`
	} `json:"attributes"`
	Relationships struct {
		Tracks struct {
			Data []trackRef `json:"data"`
		} `json:"tracks"`
	} `json:"relationships"`
}

type createPlaylistResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// CreatePlaylist creates a library playlist with songIDs in order and returns its ID.
func (c *Client) CreatePlaylist(ctx context.Context, name string, songIDs []string) (string, error) {
	var body createPlaylistRequest
	body.Attributes.Name = name
	body.Relationships.Tracks.Data = trackRefs(songIDs)

	var resp createPlaylistResponse
	if err := c.do(ctx, "POST", "/v1/me/library/playlists", nil, body, &resp); err != nil {
		return "", err
	}
	if len(resp.Data) == 0 || resp.Data[0].ID == "" {
		return "", errors.New("POST /v1/me/library/playlists: response has no playlist ID")
	}
	return resp.Data[0].ID, nil
}

type addTracksRequest struct {
	Data []trackRef `json:"data"`
}

// AddTracks appends songIDs to library playlist playlistID; an unknown ID is ErrNotFound.
func (c *Client) AddTracks(ctx context.Context, playlistID string, songIDs []string) error {
	path := "/v1/me/library/playlists/" + url.PathEscape(playlistID) + "/tracks"
	return c.do(ctx, "POST", path, nil, addTracksRequest{Data: trackRefs(songIDs)}, nil)
}

// trackRefs never returns nil, so an empty list encodes as [] and not null.
func trackRefs(songIDs []string) []trackRef {
	refs := make([]trackRef, 0, len(songIDs))
	for _, id := range songIDs {
		refs = append(refs, trackRef{ID: id, Type: "songs"})
	}
	return refs
}
