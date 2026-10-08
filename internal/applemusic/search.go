package applemusic

import (
	"context"
	"net/url"
)

// searchLimit is how many candidates the matcher gets to choose from.
const searchLimit = "5"

// Song is a catalog song as returned by search.
type Song struct {
	ID     string
	Name   string
	Artist string
	Album  string
}

type searchResponse struct {
	Results struct {
		Songs struct {
			Data []struct {
				ID         string `json:"id"`
				Attributes struct {
					Name       string `json:"name"`
					ArtistName string `json:"artistName"`
					AlbumName  string `json:"albumName"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"songs"`
	} `json:"results"`
}

// Search returns up to five catalog songs for term in storefront (e.g. "cz"), in relevance order.
func (c *Client) Search(ctx context.Context, storefront, term string) ([]Song, error) {
	path := "/v1/catalog/" + url.PathEscape(storefront) + "/search"
	q := url.Values{
		"term":  {term},
		"types": {"songs"},
		"limit": {searchLimit},
	}

	var resp searchResponse
	if err := c.do(ctx, "GET", path, q, nil, &resp); err != nil {
		return nil, err
	}

	songs := make([]Song, 0, len(resp.Results.Songs.Data))
	for _, d := range resp.Results.Songs.Data {
		songs = append(songs, Song{
			ID:     d.ID,
			Name:   d.Attributes.Name,
			Artist: d.Attributes.ArtistName,
			Album:  d.Attributes.AlbumName,
		})
	}
	return songs, nil
}
