package applemusic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestAddToLibraryBatches(t *testing.T) {
	var mu sync.Mutex
	var batches []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/me/library" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		mu.Lock()
		batches = append(batches, r.URL.Query().Get("ids[songs]"))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})

	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	if err := c.AddToLibrary(context.Background(), ids); err != nil {
		t.Fatalf("AddToLibrary() error = %v", err)
	}
	if len(batches) != 3 {
		t.Fatalf("sent %d requests, want 3 (100+100+50)", len(batches))
	}
	for i, want := range []int{100, 100, 50} {
		if got := len(strings.Split(batches[i], ",")); got != want {
			t.Errorf("batch %d has %d ids, want %d", i, got, want)
		}
	}
	if !strings.HasPrefix(batches[0], "0,1,2,") || !strings.HasSuffix(batches[2], ",249") {
		t.Errorf("ids not sent in order: %q ... %q", batches[0][:10], batches[2])
	}
}

func TestFavoriteRequest(t *testing.T) {
	var method, rawPath string
	var body []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, rawPath = r.Method, r.URL.EscapedPath()
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"data":[{"id":"724466700","type":"ratings","attributes":{"value":1}}]}`))
	})

	if err := c.Favorite(context.Background(), "724466700"); err != nil {
		t.Fatalf("Favorite() error = %v", err)
	}
	if method != http.MethodPut || rawPath != "/v1/me/ratings/songs/724466700" {
		t.Errorf("request = %s %s", method, rawPath)
	}
	assertJSONEqual(t, body, `{"type":"rating","attributes":{"value":1}}`)
}

func TestLibraryAndFavoriteErrors(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusNotFound, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tt.status) })
			for name, err := range map[string]error{
				"AddToLibrary": c.AddToLibrary(context.Background(), []string{"1"}),
				"Favorite":     c.Favorite(context.Background(), "1"),
			} {
				if !errors.Is(err, tt.want) {
					t.Errorf("%s error = %v, want %v", name, err, tt.want)
				}
				if err != nil && (strings.Contains(err.Error(), testDevToken) || strings.Contains(err.Error(), testUserToken)) {
					t.Errorf("%s error leaks a token: %v", name, err)
				}
			}
		})
	}
}

func TestAddToLibraryEmptyIsNoop(t *testing.T) {
	c := newTestClient(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("unexpected request") })
	if err := c.AddToLibrary(context.Background(), nil); err != nil {
		t.Fatalf("AddToLibrary(nil) error = %v", err)
	}
}

func TestListPlaylistsFollowsNext(t *testing.T) {
	var calls []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RequestURI())
		if r.URL.Query().Get("offset") == "" {
			_, _ = w.Write([]byte(`{"next":"/v1/me/library/playlists?offset=100","data":[
				{"id":"p.1","attributes":{"name":"Road trip","canEdit":true,"lastModifiedDate":"2026-10-07T22:18:30Z"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"p.fav","attributes":{"name":"Favourite Songs","canEdit":false}}]}`))
	})

	got, err := c.ListPlaylists(context.Background())
	if err != nil {
		t.Fatalf("ListPlaylists() error = %v", err)
	}
	want := []Playlist{
		{ID: "p.1", Name: "Road trip", CanEdit: true, Modified: "2026-10-07T22:18:30Z"},
		{ID: "p.fav", Name: "Favourite Songs"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ListPlaylists() = %+v, want %+v", got, want)
	}
	if len(calls) != 2 || calls[0] != "/v1/me/library/playlists?limit=100" || calls[1] != "/v1/me/library/playlists?limit=100&offset=100" {
		t.Errorf("requests = %v", calls)
	}
}

// A next link to anywhere but this API is not followed.
func TestListPlaylistsIgnoresForeignNext(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"next":"https://evil.example/steal","data":[]}`))
	})
	if _, err := c.ListPlaylists(context.Background()); err != nil {
		t.Fatalf("ListPlaylists() error = %v", err)
	}
	if calls != 1 {
		t.Errorf("made %d requests, want 1", calls)
	}
}

// Hitting the page cap is an error, not a short list that looks complete.
func TestListPlaylistsTooMany(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"next":"/v1/me/library/playlists?offset=1","data":[{"id":"p.x","attributes":{"name":"x"}}]}`))
	})
	if _, err := c.ListPlaylists(context.Background()); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("ListPlaylists() error = %v, want a too-many error", err)
	}
}
