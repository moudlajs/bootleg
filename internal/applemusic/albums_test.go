package applemusic

import (
	"context"
	"net/http"
	"testing"
)

func TestSearchAlbums(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/v1/catalog/cz/search" || q.Get("types") != "albums" || q.Get("term") != "sigur ros takk" {
			t.Errorf("request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"results":{"albums":{"data":[
			{"id":"697318976","attributes":{"name":"Takk...","artistName":"Sigur Rós","trackCount":11,"releaseDate":"2005-09-12"}}]}}}`))
	})
	got, err := c.SearchAlbums(context.Background(), "cz", "sigur ros takk")
	want := Album{ID: "697318976", Name: "Takk...", Artist: "Sigur Rós", TrackCount: 11, ReleaseDate: "2005-09-12"}
	if err != nil || len(got) != 1 || got[0] != want {
		t.Errorf("SearchAlbums() = %+v, %v", got, err)
	}
}

func TestAlbumTracksSkipsVideosAndPages(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "" {
			_, _ = w.Write([]byte(`{"next":"/v1/catalog/cz/albums/1/tracks?offset=1","data":[
				{"id":"10","type":"songs","attributes":{"name":"Glósóli","artistName":"Sigur Rós","albumName":"Takk..."}},
				{"id":"11","type":"music-videos","attributes":{"name":"Glósóli (video)"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"12","type":"songs","attributes":{"name":"Hoppípolla","artistName":"Sigur Rós"}}]}`))
	})
	got, err := c.AlbumTracks(context.Background(), "cz", "1")
	if err != nil || len(got) != 2 || got[0].ID != "10" || got[1].Name != "Hoppípolla" {
		t.Errorf("AlbumTracks() = %+v, %v", got, err)
	}
}
