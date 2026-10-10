package applemusic

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestUnfavorite(t *testing.T) {
	for status, want := range map[int]bool{http.StatusNoContent: true, http.StatusNotFound: false} {
		var got string
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			got = r.Method + " " + r.URL.EscapedPath()
			w.WriteHeader(status)
		})
		removed, err := c.Unfavorite(context.Background(), "1440903439")
		if err != nil || removed != want || got != "DELETE /v1/me/ratings/songs/1440903439" {
			t.Errorf("status %d: removed %v, err %v, request %q", status, removed, err, got)
		}
	}
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	if _, err := c.Unfavorite(context.Background(), "1"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401: err = %v", err)
	}
}

func TestLibraryID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/catalog/cz/songs/1440903439" || r.URL.Query().Get("relate") != "library" {
			t.Errorf("request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"1440903439","relationships":{"library":{"data":[{"id":"i.Pk"}]}}}]}`))
	})
	id, ok, err := c.LibraryID(context.Background(), "cz", "1440903439")
	if err != nil || !ok || id != "i.Pk" {
		t.Errorf("LibraryID() = %q, %v, %v", id, ok, err)
	}

	c = newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"1","relationships":{"library":{"data":[]}}}]}`))
	})
	if _, ok, err := c.LibraryID(context.Background(), "cz", "1"); ok || err != nil {
		t.Errorf("not in library: ok %v, err %v", ok, err)
	}
}

func TestPlaylistTracksAndRemove(t *testing.T) {
	var calls []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Query().Get("offset") == "":
			_, _ = w.Write([]byte(`{"next":"/v1/me/library/playlists/p.1/tracks?offset=100","data":[
				{"id":"i.a","attributes":{"name":"Crazy","playParams":{"catalogId":"135149703"}}}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"i.b","attributes":{"name":"Halo","playParams":{"catalogId":"2"}}}]}`))
		}
	})
	tracks, err := c.PlaylistTracks(context.Background(), "p.1")
	if err != nil || len(tracks) != 2 || tracks[0] != (PlaylistTrack{ID: "i.a", CatalogID: "135149703", Name: "Crazy"}) {
		t.Fatalf("PlaylistTracks() = %+v, %v", tracks, err)
	}
	if err := c.RemoveFromPlaylist(context.Background(), "p.1", "i.a"); err != nil {
		t.Fatal(err)
	}
	if want := "DELETE /v1/me/library/playlists/p.1/tracks?ids%5Blibrary-songs%5D=i.a&mode=all"; calls[len(calls)-1] != want {
		t.Errorf("remove request = %q, want %q", calls[len(calls)-1], want)
	}
}

func TestRemoveFromLibrary(t *testing.T) {
	var got string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.RemoveFromLibrary(context.Background(), "i.Pk"); err != nil || got != "DELETE /v1/me/library/songs/i.Pk" {
		t.Errorf("err %v, request %q", err, got)
	}
}
