package connector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/auth"
)

// fakeApple serves a two-song catalog and an existing "Road trip" playlist,
// and records writes. status, when set, answers every request with it.
type fakeApple struct {
	mu      sync.Mutex
	status  int
	failFav bool
	created []string
	library []string
	favs    []string
}

var catalog = map[string][2]string{ // search term -> id, "Artist|Title"
	"Portishead Glory Box": {"p1", "Portishead|Glory Box"},
	"Björk Army of Me":     {"b1", "Björk|Army of Me"},
}

func (f *fakeApple) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/search"):
		data := []any{}
		if e, ok := catalog[r.URL.Query().Get("term")]; ok {
			artist, title, _ := strings.Cut(e[1], "|")
			data = append(data, map[string]any{"id": e[0], "attributes": map[string]string{"name": title, "artistName": artist}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": map[string]any{"songs": map[string]any{"data": data}}})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/me/library/playlists":
		_, _ = w.Write([]byte(`{"data":[{"id":"p.road","attributes":{"name":"Road trip","canEdit":true}},
			{"id":"p.fav","attributes":{"name":"Favourite Songs","canEdit":false}}]}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/me/library/playlists":
		f.created = append(f.created, "p.new")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":[{"id":"p.new"}]}`))
	case r.URL.Path == "/v1/me/library":
		f.library = append(f.library, r.URL.Query().Get("ids[songs]"))
		w.WriteHeader(http.StatusAccepted)
	case strings.HasPrefix(r.URL.Path, "/v1/me/ratings/songs/"):
		if f.failFav {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.favs = append(f.favs, strings.TrimPrefix(r.URL.Path, "/v1/me/ratings/songs/"))
		_, _ = w.Write([]byte(`{"data":[]}`))
	default:
		http.NotFound(w, r)
	}
}

func newService(t *testing.T, f *fakeApple) *Service {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Service{
		API:        applemusic.New(srv.Client(), srv.URL, "FAKE-DEV", "FAKE-USER"),
		Storefront: "cz",
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// connect runs the MCP server in-process and returns a client session.
func connect(t *testing.T, svc *Service) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := sdk.NewInMemoryTransports()
	ss, err := NewServer(svc, "test").Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call invokes a tool and returns its structured result (decoded into out)
// or its error text.
func call(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any, out any) (errText string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", tool, err)
	}
	if res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				errText += tc.Text
			}
		}
		return errText
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s result: %v", tool, err)
	}
	return ""
}

const songs = "# from a screenshot\nPortishead - Glory Box\nNobody - Nothing\nBjörk - Army of Me\n"

func TestPreviewSongs(t *testing.T) {
	f := &fakeApple{}
	var out PreviewOutput
	if e := call(t, connect(t, newService(t, f)), "preview_songs", map[string]any{"songs": songs}, &out); e != "" {
		t.Fatalf("error: %s", e)
	}
	if len(out.Matched) != 2 || out.Matched[0].ID != "p1" || out.Matched[1].Title != "Army of Me" {
		t.Errorf("matched = %+v", out.Matched)
	}
	if len(out.Unmatched) != 1 || out.Unmatched[0].Line != 3 || out.Skipped != 1 {
		t.Errorf("unmatched = %+v, skipped %d", out.Unmatched, out.Skipped)
	}
	if len(f.created)+len(f.library)+len(f.favs) != 0 {
		t.Error("preview wrote something")
	}
}

func TestAddSongsNewPlaylistAndFavourites(t *testing.T) {
	f := &fakeApple{}
	var out AddOutput
	e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{
		"songs": songs, "to": []string{"pl", "fav"}, "playlist_name": "Mix",
	}, &out)
	if e != "" {
		t.Fatalf("error: %s", e)
	}
	if out.Matched != 2 || len(out.Unmatched) != 1 || out.Favourited != 2 || out.AddedToLibrary != 0 {
		t.Errorf("out = %+v", out)
	}
	if out.Playlist == nil || out.Playlist.ID != "p.new" || !out.Playlist.Created || out.Playlist.Name != "Mix" || out.Playlist.Songs != 2 {
		t.Errorf("playlist = %+v", out.Playlist)
	}
	if len(f.created) != 1 || strings.Join(f.favs, ",") != "p1,b1" {
		t.Errorf("server saw created %v, favs %v", f.created, f.favs)
	}
}

func TestAddSongsRefusesDuplicatePlaylistName(t *testing.T) {
	f := &fakeApple{}
	e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{
		"songs": songs, "to": []string{"pl"}, "playlist_name": " road TRIP ",
	}, &AddOutput{})
	if !strings.Contains(e, "already exists") || !strings.Contains(e, "p.road") {
		t.Errorf("error = %q, want it to name the existing playlist", e)
	}
	if len(f.created) != 0 {
		t.Error("created a duplicate playlist")
	}
}

func TestAddSongsValidation(t *testing.T) {
	many := strings.Repeat("A - B\n", maxSongs+1)
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no destination", map[string]any{"songs": songs}, `missing properties: ["to"]`}, // the SDK's schema check
		{"empty destination", map[string]any{"songs": songs, "to": []string{}}, "where the songs go"},
		{"unknown destination", map[string]any{"songs": songs, "to": []string{"cloud"}}, "unknown destination"},
		{"pl without a playlist", map[string]any{"songs": songs, "to": []string{"pl"}}, "playlist_name"},
		{"both name and id", map[string]any{"songs": songs, "to": []string{"pl"}, "playlist_name": "x", "playlist_id": "p.1"}, "not both"},
		{"name without pl", map[string]any{"songs": songs, "to": []string{"lib"}, "playlist_name": "x"}, "only apply with pl"},
		{"no songs", map[string]any{"songs": "# nothing\n", "to": []string{"lib"}}, "no songs given"},
		{"too many", map[string]any{"songs": many, "to": []string{"lib"}}, "split the list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeApple{}
			e := call(t, connect(t, newService(t, f)), "add_songs", tt.args, &AddOutput{})
			if !strings.Contains(e, tt.want) {
				t.Errorf("error = %q, want it to contain %q", e, tt.want)
			}
			if len(f.created)+len(f.library)+len(f.favs) != 0 {
				t.Error("wrote something despite invalid arguments")
			}
		})
	}
}

func TestExpiredTokensSayHowToRefresh(t *testing.T) {
	e := call(t, connect(t, newService(t, &fakeApple{status: http.StatusUnauthorized})), "preview_songs",
		map[string]any{"songs": songs}, &PreviewOutput{})
	if !strings.Contains(e, "expired") || !strings.Contains(e, "deploy/tokens.sh") {
		t.Errorf("error = %q, want the refresh steps", e)
	}
	if strings.Contains(e, "FAKE-DEV") || strings.Contains(e, "FAKE-USER") {
		t.Errorf("error leaks a token: %q", e)
	}
}

func TestIncompleteWriteSaysHowToFinish(t *testing.T) {
	f := &fakeApple{failFav: true}
	e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{
		"songs": songs, "to": []string{"lib", "fav"},
	}, &AddOutput{})
	if !strings.Contains(e, "Already done: library") || !strings.Contains(e, "to=[fav]") {
		t.Errorf("error = %q, want what's done and to=[fav]", e)
	}
}

func TestListPlaylists(t *testing.T) {
	var out PlaylistsOutput
	if e := call(t, connect(t, newService(t, &fakeApple{})), "list_playlists", map[string]any{}, &out); e != "" {
		t.Fatalf("error: %s", e)
	}
	want := []PlaylistInfo{{ID: "p.road", Name: "Road trip", CanAdd: true}, {ID: "p.fav", Name: "Favourite Songs"}}
	if len(out.Playlists) != 2 || out.Playlists[0] != want[0] || out.Playlists[1] != want[1] {
		t.Errorf("playlists = %+v", out.Playlists)
	}
}

func TestHTTPHandler(t *testing.T) {
	signIn, err := auth.New(auth.Config{
		BaseURL:    "http://127.0.0.1",
		Passphrase: "correct horse battery",
		SigningKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(HTTPHandler(NewServer(newService(t, &fakeApple{}), "v9"), "v9", signIn))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "v9") {
		t.Errorf("/health = %d %q", resp.StatusCode, body)
	}

	// Strangers can't drain the owner's rate limit: more unauthenticated
	// requests than the burst all get 401, never 429.
	for i := 0; i < requestBurst+10; i++ {
		resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated request %d = %d, want 401", i, resp.StatusCode)
		}
	}

	// Without a token, /mcp refuses and points at the sign-in metadata.
	resp, err = http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
		t.Errorf("/mcp without a token = %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

// add_songs to a playlist ID that isn't in the library says so; elsewhere a
// 404 isn't blamed on a playlist.
func TestNotFoundWording(t *testing.T) {
	f := &fakeApple{}
	svc := newService(t, f)
	e := call(t, connect(t, svc), "add_songs", map[string]any{
		"songs": songs, "to": []string{"pl"}, "playlist_id": "p.gone",
	}, &AddOutput{})
	if !strings.Contains(e, "p.gone isn't in the library") {
		t.Errorf("error = %q", e)
	}
	if got := people(applemusic.ErrNotFound); strings.Contains(got, "playlist") {
		t.Errorf("people(ErrNotFound) = %q, should not mention playlists", got)
	}
}
