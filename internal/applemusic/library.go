package applemusic

import (
	"context"
	"net/url"
	"strings"
)

// libraryBatch caps song IDs per request to keep the query string short.
const libraryBatch = 100

// AddToLibrary adds catalog songs to the user's library; Apple applies it within seconds.
func (c *Client) AddToLibrary(ctx context.Context, songIDs []string) error {
	for start := 0; start < len(songIDs); start += libraryBatch {
		end := min(start+libraryBatch, len(songIDs))
		q := url.Values{"ids[songs]": {strings.Join(songIDs[start:end], ",")}}
		if err := c.do(ctx, "POST", "/v1/me/library", q, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

type ratingRequest struct {
	Type       string `json:"type"`
	Attributes struct {
		Value int `json:"value"`
	} `json:"attributes"`
}

// Favorite favourites a catalog song (a "love" rating of 1), which also adds it to the library.
func (c *Client) Favorite(ctx context.Context, songID string) error {
	body := ratingRequest{Type: "rating"}
	body.Attributes.Value = 1
	path := "/v1/me/ratings/songs/" + url.PathEscape(songID)
	return c.do(ctx, "PUT", path, nil, body, nil)
}
