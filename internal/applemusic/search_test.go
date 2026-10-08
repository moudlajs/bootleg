package applemusic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Obviously fake tokens, so a leak into an error message is easy to spot.
const (
	testDevToken  = "FAKE-DEV-TOKEN"
	testUserToken = "FAKE-USER-TOKEN"
)

// newTestClient returns a Client pointed at an httptest.Server running handler.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(srv.Client(), srv.URL, testDevToken, testUserToken)
}

const twoSongs = `{"results":{"songs":{"data":[
  {"id":"1","type":"songs","attributes":{"name":"Army of Me","artistName":"Björk","albumName":"Post"}},
  {"id":"2","type":"songs","attributes":{"name":"Army of Me (Karaoke)","artistName":"Karaoke Stars","albumName":"Hits"}}
]}}}`

func TestSearch(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		want      []Song
		wantErrIs error  // sentinel expected via errors.Is
		wantErr   string // substring expected when there is no sentinel
	}{
		{
			name:   "success",
			status: http.StatusOK,
			body:   twoSongs,
			want: []Song{
				{ID: "1", Name: "Army of Me", Artist: "Björk", Album: "Post"},
				{ID: "2", Name: "Army of Me (Karaoke)", Artist: "Karaoke Stars", Album: "Hits"},
			},
		},
		{
			name:   "empty results object",
			status: http.StatusOK,
			body:   `{"results":{}}`,
			want:   []Song{},
		},
		{
			name:   "empty songs data",
			status: http.StatusOK,
			body:   `{"results":{"songs":{"data":[]}}}`,
			want:   []Song{},
		},
		{name: "401", status: http.StatusUnauthorized, wantErrIs: ErrUnauthorized},
		{name: "403", status: http.StatusForbidden, wantErrIs: ErrUnauthorized},
		{name: "429", status: http.StatusTooManyRequests, wantErrIs: ErrRateLimited},
		{name: "500", status: http.StatusInternalServerError, wantErr: "unexpected status 500"},
		{name: "malformed JSON", status: http.StatusOK, body: `{"results":`, wantErr: "decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			got, err := c.Search(context.Background(), "cz", "Björk Army of Me")

			switch {
			case tt.wantErrIs != nil:
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("Search() error = %v, want errors.Is %v", err, tt.wantErrIs)
				}
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Search() error = %v, want containing %q", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Search() error = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Search() = %#v, want %#v", got, tt.want)
				}
			}

			if err != nil && (strings.Contains(err.Error(), testDevToken) || strings.Contains(err.Error(), testUserToken)) {
				t.Errorf("error leaks a token: %v", err)
			}
		})
	}
}

func TestSearchRequest(t *testing.T) {
	var got *http.Request
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = w.Write([]byte(`{"results":{}}`))
	})

	if _, err := c.Search(context.Background(), "cz", "Sigur Rós & friends"); err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if got.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.Method)
	}
	if got.URL.Path != "/v1/catalog/cz/search" {
		t.Errorf("path = %s", got.URL.Path)
	}
	// Decoded via URL.Query(), so this also proves & and non-ASCII survived encoding.
	q := got.URL.Query()
	for k, want := range map[string]string{"term": "Sigur Rós & friends", "types": "songs", "limit": "5"} {
		if q.Get(k) != want {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), want)
		}
	}

	headers := map[string]string{
		"Authorization":    "Bearer " + testDevToken,
		"Media-User-Token": testUserToken,
		"Origin":           "https://music.apple.com",
	}
	for k, want := range headers {
		if v := got.Header.Get(k); v != want {
			t.Errorf("header %s = %q, want %q", k, v, want)
		}
	}
}

func TestSearchCancelledContext(t *testing.T) {
	// atomic: the handler runs on the server's goroutine, not the test's.
	var called atomic.Bool
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		_, _ = w.Write([]byte(`{"results":{}}`))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Search(ctx, "cz", "anything")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context.Canceled", err)
	}
	if called.Load() {
		t.Error("server was called despite a cancelled context")
	}
}

func TestClientNeverPrintsTokens(t *testing.T) {
	c := New(http.DefaultClient, "https://example.test", testDevToken, testUserToken)

	var logBuf bytes.Buffer
	slog.New(slog.NewTextHandler(&logBuf, nil)).Info("client", "c", c)

	for name, out := range map[string]string{
		"%v":   fmt.Sprintf("%v", c),
		"%+v":  fmt.Sprintf("%+v", c),
		"%#v":  fmt.Sprintf("%#v", c),
		"slog": logBuf.String(),
	} {
		if strings.Contains(out, testDevToken) || strings.Contains(out, testUserToken) {
			t.Errorf("%s leaks a token: %s", name, out)
		}
	}
}
