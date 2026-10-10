package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/auth"
)

// fakeApple serves a two-song catalog and a "Road trip" playlist; status, if set, answers everything.
type fakeApple struct {
	mu       sync.Mutex
	status   int
	failFav  bool
	searches int
	isFav    map[string]bool // favourited catalog IDs, for remove_songs
	inLib    map[string]bool // catalog IDs with a library copy
	removed  []string        // what remove_songs deleted, in order
	created  []string
	library  []string
	favs     []string
}

var catalog = map[string][2]string{ // search term -> id, "Artist|Title"
	"Portishead Glory Box": {"1001", "Portishead|Glory Box"},
	"Björk Army of Me":     {"1002", "Björk|Army of Me"},
	"Portished Glory Box":  {"1001", "Portishead|Glory Box"}, // typo: artist doesn't match
}

func (f *fakeApple) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/search") && r.URL.Query().Get("types") == "albums":
		_, _ = w.Write([]byte(`{"results":{"albums":{"data":[
			{"id":"2002","attributes":{"name":"Takk... (Deluxe Edition)","artistName":"Sigur Rós","trackCount":16}},
			{"id":"2001","attributes":{"name":"Takk...","artistName":"Sigur Rós","trackCount":2,"releaseDate":"2005-09-12"}}]}}}`))
	case r.URL.Path == "/v1/catalog/cz/albums/2001/tracks":
		_, _ = w.Write([]byte(`{"data":[
			{"id":"3001","type":"songs","attributes":{"name":"Glósóli","artistName":"Sigur Rós","albumName":"Takk..."}},
			{"id":"3002","type":"songs","attributes":{"name":"Hoppípolla","artistName":"Sigur Rós","albumName":"Takk..."}}]}`))
	case strings.HasPrefix(r.URL.Path, "/v1/catalog/cz/albums/"):
		w.WriteHeader(http.StatusNotFound)
	case strings.HasSuffix(r.URL.Path, "/search"):
		f.searches++
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
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/me/ratings/songs/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/me/ratings/songs/")
		if !f.isFav[id] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"` + id + `","attributes":{"value":1}}]}`))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/me/ratings/songs/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/me/ratings/songs/")
		delete(f.isFav, id)
		f.removed = append(f.removed, "fav:"+id)
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(r.URL.Path, "/v1/catalog/cz/songs/") && r.URL.Query().Get("relate") == "library":
		id := strings.TrimPrefix(r.URL.Path, "/v1/catalog/cz/songs/")
		lib := []any{}
		if f.inLib[id] {
			lib = append(lib, map[string]string{"id": "i." + id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": id,
			"relationships": map[string]any{"library": map[string]any{"data": lib}}}}})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/me/library/songs/"):
		f.removed = append(f.removed, "lib:"+strings.TrimPrefix(r.URL.Path, "/v1/me/library/songs/"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/me/library/playlists/p.road/tracks":
		_, _ = w.Write([]byte(`{"data":[{"id":"i.t1","attributes":{"name":"Glory Box","playParams":{"catalogId":"1001"}}}]}`))
	case r.Method == http.MethodDelete && r.URL.Path == "/v1/me/library/playlists/p.road/tracks":
		f.removed = append(f.removed, "pl:"+r.URL.Query().Get("ids[library-songs]")+":"+r.URL.Query().Get("mode"))
		w.WriteHeader(http.StatusNoContent)
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

// call invokes a tool, decoding its result into out, or returns its error text.
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
	if len(out.Matched) != 2 || out.Matched[0].ID != "1001" || out.Matched[1].Title != "Army of Me" {
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
	if len(f.created) != 1 || strings.Join(f.favs, ",") != "1001,1002" {
		t.Errorf("server saw created %v, favs %v", f.created, f.favs)
	}
	if !strings.Contains(out.Note, "few minutes") {
		t.Errorf("note = %q, want the sync-delay hint", out.Note)
	}
}

// No favourites, no note: the delay hint is only for favourites.
func TestAddSongsLibraryHasNoNote(t *testing.T) {
	var out AddOutput
	if e := call(t, connect(t, newService(t, &fakeApple{})), "add_songs", map[string]any{
		"songs": songs, "to": []string{"lib"},
	}, &out); e != "" {
		t.Fatalf("error: %s", e)
	}
	if out.AddedToLibrary != 2 || out.Note != "" {
		t.Errorf("out = %+v, want 2 in the library and no note", out)
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

	// Strangers can't drain the owner's rate limit: past the burst it's still 401, never 429.
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

// A missing playlist ID is reported as such; other 404s aren't blamed on a playlist.
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

func TestToolNamesMatchRegisteredTools(t *testing.T) {
	cs := connect(t, newService(t, &fakeApple{}))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range res.Tools {
		got = append(got, tl.Name)
	}
	slices.Sort(got)
	want := slices.Clone(toolNames)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("registered %v, toolNames %v", got, want)
	}
}

func TestServerInfoInFirstTools(t *testing.T) {
	cs := connect(t, newService(t, &fakeApple{}))
	var p PreviewOutput
	if e := call(t, cs, "preview_songs", map[string]any{"songs": songs}, &p); e != "" {
		t.Fatalf("preview error: %s", e)
	}
	var l PlaylistsOutput
	if e := call(t, cs, "list_playlists", map[string]any{}, &l); e != "" {
		t.Fatalf("list_playlists error: %s", e)
	}
	for name, info := range map[string]ServerInfo{"preview_songs": p.Server, "list_playlists": l.Server} {
		if info.Version != "test" || len(info.Tools) != len(toolNames) || !strings.Contains(info.Note, "reconnect") {
			t.Errorf("%s server = %+v", name, info)
		}
	}
}

func TestPreviewOffersAlternatives(t *testing.T) {
	var out PreviewOutput
	if e := call(t, connect(t, newService(t, &fakeApple{})), "preview_songs", map[string]any{"songs": "Portished - Glory Box"}, &out); e != "" {
		t.Fatalf("error: %s", e)
	}
	if len(out.Unmatched) != 1 || len(out.Unmatched[0].Alternatives) != 1 || out.Unmatched[0].Alternatives[0].ID != "1001" {
		t.Fatalf("unmatched = %+v, want Glory Box (1001) offered", out.Unmatched)
	}
}

func TestAddBySongIDs(t *testing.T) {
	f := &fakeApple{}
	var out AddOutput
	e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{
		"songs": "Björk - Army of Me", "song_ids": []string{"1001", "1002"}, "to": []string{"lib"},
	}, &out)
	if e != "" {
		t.Fatalf("error: %s", e)
	}
	// 1002 comes from the line and is not added twice.
	if out.Matched != 2 || strings.Join(f.library, ";") != "1002,1001" {
		t.Errorf("out = %+v, library %v", out, f.library)
	}

	f = &fakeApple{}
	if e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{"song_ids": []string{"1001"}, "to": []string{"fav"}}, &out); e != "" {
		t.Fatalf("ids only: %s", e)
	}
	if strings.Join(f.favs, ",") != "1001" {
		t.Errorf("favs = %v", f.favs)
	}
}

func TestAddCapsCombinedSongs(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprint(5000 + i)
		}
		return out
	}
	tests := []struct {
		name         string
		songs        string
		n            int
		wantErr      string
		wantSearches int
	}{
		{"51 IDs: refused before searching", "", 51, "more than 50", 0},
		{"50 IDs + a line: refused after it", "Björk - Army of Me", 50, "more than 50", 1},
		{"50 IDs + comments only: fits", "# nothing but a comment", 50, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeApple{}
			e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{"songs": tt.songs, "song_ids": ids(tt.n), "to": []string{"lib"}}, &AddOutput{})
			if (tt.wantErr == "" && e != "") || !strings.Contains(e, tt.wantErr) || f.searches != tt.wantSearches {
				t.Errorf("error %q (want %q), searches %d (want %d)", e, tt.wantErr, f.searches, tt.wantSearches)
			}
		})
	}
}

func TestAddRejectsBadSongIDs(t *testing.T) {
	f := &fakeApple{}
	e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{"song_ids": []string{"12/../x"}, "to": []string{"lib"}}, &AddOutput{})
	if !strings.Contains(e, "not a catalog song ID") || len(f.library) != 0 {
		t.Errorf("error = %q, library %v", e, f.library)
	}
	if e := call(t, connect(t, newService(t, f)), "add_songs", map[string]any{"to": []string{"lib"}}, &AddOutput{}); !strings.Contains(e, "no songs given") {
		t.Errorf("empty: %q", e)
	}
}

func TestServerInfoTokenExpiry(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	if info := serverInfo("v", time.Time{}, now); info.TokenExpires != "" || strings.Contains(info.Note, "expires") {
		t.Errorf("unknown expiry: %+v", info)
	}
	if info := serverInfo("v", now.AddDate(0, 2, 0), now); info.TokenExpires != "2026-12-10" || strings.Contains(info.Note, "expires on") {
		t.Errorf("far expiry: %+v", info)
	}
	if info := serverInfo("v", now.AddDate(0, 0, 3), now); !strings.Contains(info.Note, "expires on 2026-10-13") || !strings.Contains(info.Note, "deploy/tokens.sh") {
		t.Errorf("near expiry: %+v", info)
	}
}

func TestRemoveSongs(t *testing.T) {
	f := &fakeApple{isFav: map[string]bool{"1001": true}, inLib: map[string]bool{"1001": true}}
	var out RemoveOutput
	e := call(t, connect(t, newService(t, f)), "remove_songs", map[string]any{
		"songs": "Portishead - Glory Box\nNobody - Nothing", "song_ids": []string{"1002"},
		"from": []string{"lib", "fav", "pl"}, "playlist_id": "p.road",
	}, &out)
	if e != "" {
		t.Fatalf("error: %s", e)
	}
	// Order: favourite, playlist, library. 1002 is nowhere, so it's reported, not an error.
	if strings.Join(f.removed, ",") != "fav:1001,pl:i.t1:all,lib:i.1001" {
		t.Errorf("removed %v", f.removed)
	}
	if out.Unfavourited != 1 || out.RemovedFromPlaylist != 1 || out.RemovedFromLibrary != 1 || len(out.NotThere) != 3 || len(out.Unmatched) != 1 {
		t.Errorf("out = %+v", out)
	}
}

func TestRemoveSongsValidation(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no from", map[string]any{"song_ids": []string{"1001"}, "from": []string{}}, "where to remove from"},
		{"pl without id", map[string]any{"song_ids": []string{"1001"}, "from": []string{"pl"}}, "needs playlist_id"},
		{"id without pl", map[string]any{"song_ids": []string{"1001"}, "from": []string{"fav"}, "playlist_id": "p.road"}, "only applies with pl"},
		{"bad id", map[string]any{"song_ids": []string{"x/../1"}, "from": []string{"fav"}}, "not a catalog song ID"},
		{"nothing", map[string]any{"from": []string{"fav"}}, "no songs given"},
		{"only unmatched", map[string]any{"songs": "Nobody - Nothing", "from": []string{"fav"}}, "nothing was removed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeApple{isFav: map[string]bool{"1001": true}}
			if e := call(t, connect(t, newService(t, f)), "remove_songs", tt.args, &RemoveOutput{}); !strings.Contains(e, tt.want) {
				t.Errorf("error = %q, want %q", e, tt.want)
			}
			if len(f.removed) != 0 {
				t.Errorf("removed %v despite invalid arguments", f.removed)
			}
		})
	}
}

func TestAlbumTracks(t *testing.T) {
	var out AlbumOutput
	if e := call(t, connect(t, newService(t, &fakeApple{})), "album_tracks", map[string]any{"album": "Sigur Ros - Takk"}, &out); e != "" {
		t.Fatalf("error: %s", e)
	}
	// The plain edition wins over the deluxe one listed first, which goes to other_editions.
	if out.Album == nil || out.Album.ID != "2001" || out.Album.Tracks != 2 {
		t.Fatalf("album = %+v", out.Album)
	}
	if len(out.Tracks) != 2 || out.Tracks[0].ID != "3001" || out.Tracks[1].Line != 2 {
		t.Errorf("tracks = %+v", out.Tracks)
	}
	if len(out.OtherEditions) != 1 || out.OtherEditions[0].ID != "2002" || !strings.Contains(out.Note, "song_ids") {
		t.Errorf("other editions %+v, note %q", out.OtherEditions, out.Note)
	}
}

func TestAlbumTracksByIDAndErrors(t *testing.T) {
	cs := connect(t, newService(t, &fakeApple{}))
	var out AlbumOutput
	if e := call(t, cs, "album_tracks", map[string]any{"album_id": "2001"}, &out); e != "" || len(out.Tracks) != 2 || out.Album.Name != "Takk..." {
		t.Errorf("by id: error %q, out %+v", e, out)
	}
	tests := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "say which album"},
		{map[string]any{"album": "x", "album_id": "2001"}, "not both"},
		{map[string]any{"album_id": "../1"}, "catalog album ID"},
		{map[string]any{"album_id": "9999"}, "isn't in the catalog"},
	}
	for _, tt := range tests {
		if e := call(t, cs, "album_tracks", tt.args, &AlbumOutput{}); !strings.Contains(e, tt.want) {
			t.Errorf("%v: error %q, want %q", tt.args, e, tt.want)
		}
	}
	// No match is not an error: the editions are offered instead.
	out = AlbumOutput{}
	if e := call(t, cs, "album_tracks", map[string]any{"album": "Björk - Post"}, &out); e != "" || out.Album != nil || len(out.OtherEditions) != 2 {
		t.Errorf("no match: error %q, out %+v", e, out)
	}
}
