package applemusic

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// maxPlaylistPages: past it ListPlaylists fails, since the duplicate-name check needs a complete list.
const maxPlaylistPages = 20

// Playlist is a playlist in the user's library.
type Playlist struct {
	ID       string // library ID, e.g. p.AbC123
	Name     string
	CanEdit  bool // false for playlists the user can't add to, e.g. Favourite Songs
	Modified string
}

type playlistsResponse struct {
	Next string `json:"next"`
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name             string `json:"name"`
			CanEdit          bool   `json:"canEdit"`
			LastModifiedDate string `json:"lastModifiedDate"`
		} `json:"attributes"`
	} `json:"data"`
}

// ListPlaylists returns the playlists in the user's library, following pagination.
func (c *Client) ListPlaylists(ctx context.Context) ([]Playlist, error) {
	var out []Playlist
	path, q := "/v1/me/library/playlists", url.Values{"limit": {"100"}}
	for page := 0; page < maxPlaylistPages && path != ""; page++ {
		var resp playlistsResponse
		if err := c.do(ctx, "GET", path, q, nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			out = append(out, Playlist{
				ID:       d.ID,
				Name:     d.Attributes.Name,
				CanEdit:  d.Attributes.CanEdit,
				Modified: d.Attributes.LastModifiedDate,
			})
		}
		// Apple's next link drops limit, so put it back to keep 100 per page.
		path, q = splitNext(resp.Next)
		if path != "" && q.Get("limit") == "" {
			q.Set("limit", "100")
		}
	}
	if path != "" {
		return nil, fmt.Errorf("more than %d playlists; not reading further", maxPlaylistPages*100)
	}
	return out, nil
}

// splitNext only follows links on the same API.
func splitNext(next string) (string, url.Values) {
	if next == "" || !strings.HasPrefix(next, "/v1/") {
		return "", nil
	}
	u, err := url.Parse(next)
	if err != nil {
		return "", nil
	}
	return u.Path, u.Query()
}
