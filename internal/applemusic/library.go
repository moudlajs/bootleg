package applemusic

import (
	"context"
	"net/url"
	"strings"
)

// libraryBatch is how many song IDs go into one add-to-library request,
// keeping the query string a reasonable length.
const libraryBatch = 100

// AddToLibrary adds catalog songs to the user's library ("Songs" in the
// app). Apple accepts the request (202) and applies it within seconds.
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

// Favorite marks a catalog song as a favourite (the star in the app), which
// puts it in the automatic "Favourite Songs" playlist and in the library.
// Under the hood it is a "love" rating of 1.
func (c *Client) Favorite(ctx context.Context, songID string) error {
	body := ratingRequest{Type: "rating"}
	body.Attributes.Value = 1
	path := "/v1/me/ratings/songs/" + url.PathEscape(songID)
	return c.do(ctx, "PUT", path, nil, body, nil)
}
