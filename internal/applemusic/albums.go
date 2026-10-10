package applemusic

import (
	"context"
	"net/url"
)

// Album is a catalog album as returned by search.
type Album struct {
	ID          string
	Name        string
	Artist      string
	TrackCount  int
	ReleaseDate string
}

type albumSearchResponse struct {
	Results struct {
		Albums struct {
			Data []struct {
				ID         string `json:"id"`
				Attributes struct {
					Name        string `json:"name"`
					ArtistName  string `json:"artistName"`
					TrackCount  int    `json:"trackCount"`
					ReleaseDate string `json:"releaseDate"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"albums"`
	} `json:"results"`
}

// SearchAlbums returns up to five catalog albums for term, in relevance order.
func (c *Client) SearchAlbums(ctx context.Context, storefront, term string) ([]Album, error) {
	var resp albumSearchResponse
	q := url.Values{"term": {term}, "types": {"albums"}, "limit": {searchLimit}}
	if err := c.do(ctx, "GET", "/v1/catalog/"+url.PathEscape(storefront)+"/search", q, nil, &resp); err != nil {
		return nil, err
	}
	albums := make([]Album, 0, len(resp.Results.Albums.Data))
	for _, d := range resp.Results.Albums.Data {
		a := d.Attributes
		albums = append(albums, Album{ID: d.ID, Name: a.Name, Artist: a.ArtistName, TrackCount: a.TrackCount, ReleaseDate: a.ReleaseDate})
	}
	return albums, nil
}

type albumTracksResponse struct {
	Next string `json:"next"`
	Data []struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Attributes struct {
			Name       string `json:"name"`
			ArtistName string `json:"artistName"`
			AlbumName  string `json:"albumName"`
		} `json:"attributes"`
	} `json:"data"`
}

// AlbumTracks returns an album's songs in album order (music videos are skipped).
func (c *Client) AlbumTracks(ctx context.Context, storefront, albumID string) ([]Song, error) {
	var out []Song
	path := "/v1/catalog/" + url.PathEscape(storefront) + "/albums/" + url.PathEscape(albumID) + "/tracks"
	q := url.Values{"limit": {"100"}}
	for page := 0; page < maxPlaylistPages && path != ""; page++ {
		var resp albumTracksResponse
		if err := c.do(ctx, "GET", path, q, nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Data {
			if d.Type != "" && d.Type != "songs" {
				continue
			}
			out = append(out, Song{ID: d.ID, Name: d.Attributes.Name, Artist: d.Attributes.ArtistName, Album: d.Attributes.AlbumName})
		}
		path, q = splitNext(resp.Next)
	}
	return out, nil
}
