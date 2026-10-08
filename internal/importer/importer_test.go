package importer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/parser"
)

// fake answers search from a tiny catalog and records writes; failOn makes one endpoint 401.
type fake struct {
	mu      sync.Mutex
	failOn  string
	created []string
	library []string
	favs    []string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/search"):
		data := []any{}
		if r.URL.Query().Get("term") == "Portishead Glory Box" {
			data = append(data, map[string]any{"id": "p1", "attributes": map[string]string{"name": "Glory Box", "artistName": "Portishead"}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": map[string]any{"songs": map[string]any{"data": data}}})
	case r.URL.Path == "/v1/me/library/playlists":
		if f.failOn == "create" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.created = append(f.created, "p.new")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":[{"id":"p.new"}]}`))
	case r.URL.Path == "/v1/me/library":
		if f.failOn == "library" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.library = append(f.library, r.URL.Query().Get("ids[songs]"))
		w.WriteHeader(http.StatusAccepted)
	case strings.HasPrefix(r.URL.Path, "/v1/me/ratings/songs/"):
		if f.failOn == "fav" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.favs = append(f.favs, strings.TrimPrefix(r.URL.Path, "/v1/me/ratings/songs/"))
		_, _ = w.Write([]byte(`{"data":[]}`))
	default:
		http.NotFound(w, r)
	}
}

func newAPI(t *testing.T, f *fake) *applemusic.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return applemusic.New(srv.Client(), srv.URL, "fake-dev", "fake-user")
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestResolveAndSplit(t *testing.T) {
	api := newAPI(t, &fake{})
	queries := []parser.Query{
		{Artist: "Nobody", Title: "Nothing", Raw: "Nobody - Nothing", Line: 1},
		{Artist: "Portishead", Title: "Glory Box", Raw: "Portishead - Glory Box", Line: 2},
	}
	lines, err := Resolve(context.Background(), api, "cz", 0, queries, quiet())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(lines) != 2 || lines[0].OK || !lines[1].OK || lines[1].Song.ID != "p1" {
		t.Fatalf("lines = %+v", lines)
	}
	ids, unmatched := Split(lines)
	if strings.Join(ids, ",") != "p1" || len(unmatched) != 1 || unmatched[0].Line != 1 {
		t.Errorf("Split() = %v, %v", ids, unmatched)
	}
}

func TestWriteAllTargets(t *testing.T) {
	f := &fake{}
	w, err := Write(context.Background(), newAPI(t, f), WriteOptions{
		To: Targets{Playlist: true, Library: true, Favorites: true}, PlaylistName: "Mix",
	}, []string{"a", "b"}, quiet())
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	want := Written{PlaylistID: "p.new", PlaylistCreated: true, Playlist: 2, Library: 2, Favorites: 2}
	if w != want {
		t.Errorf("Written = %+v, want %+v", w, want)
	}
	if len(f.created) != 1 || strings.Join(f.library, ";") != "a,b" || strings.Join(f.favs, ",") != "a,b" {
		t.Errorf("server saw created %v, library %v, favs %v", f.created, f.library, f.favs)
	}
}

func TestWriteIncomplete(t *testing.T) {
	tests := []struct {
		failOn   string
		wantDone []string
		wantRest []string
		written  Written
	}{
		{"library", []string{"playlist"}, []string{"lib", "fav"}, Written{PlaylistID: "p.new", PlaylistCreated: true, Playlist: 1}},
		{"fav", []string{"playlist", "library"}, []string{"fav"}, Written{PlaylistID: "p.new", PlaylistCreated: true, Playlist: 1, Library: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.failOn, func(t *testing.T) {
			w, err := Write(context.Background(), newAPI(t, &fake{failOn: tt.failOn}), WriteOptions{
				To: Targets{Playlist: true, Library: true, Favorites: true}, PlaylistName: "Mix",
			}, []string{"a"}, quiet())
			var inc *IncompleteError
			if !errors.As(err, &inc) {
				t.Fatalf("error = %v, want *IncompleteError", err)
			}
			if strings.Join(inc.Done, ",") != strings.Join(tt.wantDone, ",") || strings.Join(inc.Rest, ",") != strings.Join(tt.wantRest, ",") {
				t.Errorf("Done %v Rest %v, want %v %v", inc.Done, inc.Rest, tt.wantDone, tt.wantRest)
			}
			if !errors.Is(err, applemusic.ErrUnauthorized) {
				t.Errorf("error %v lost its cause", err)
			}
			if w != tt.written {
				t.Errorf("Written = %+v, want %+v", w, tt.written)
			}
		})
	}
}

// A failure at the first step is a plain error: nothing to resume.
func TestWriteFirstStepFailsPlainly(t *testing.T) {
	_, err := Write(context.Background(), newAPI(t, &fake{failOn: "create"}), WriteOptions{
		To: Targets{Playlist: true, Favorites: true}, PlaylistName: "Mix",
	}, []string{"a"}, quiet())
	var inc *IncompleteError
	if errors.As(err, &inc) || !errors.Is(err, applemusic.ErrUnauthorized) {
		t.Fatalf("error = %v, want a plain ErrUnauthorized", err)
	}
}

func TestParseTargets(t *testing.T) {
	tests := []struct {
		in      string
		want    Targets
		wantErr bool
	}{
		{"pl", Targets{Playlist: true}, false},
		{"lib", Targets{Library: true}, false},
		{"fav", Targets{Favorites: true}, false},
		{"lib,fav", Targets{Library: true, Favorites: true}, false},
		{" FAV , Library ,pl,pl", Targets{Playlist: true, Library: true, Favorites: true}, false},
		{"favourites", Targets{Favorites: true}, false},
		{"lib,", Targets{}, true}, // trailing comma: empty entry
		{"cloud", Targets{}, true},
	}
	for _, tt := range tests {
		got, err := ParseTargets(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseTargets(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseTargets(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestSleep(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("Sleep() = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep() = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Error("sleep ignored the cancelled context")
	}
}
