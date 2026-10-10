// Package connector exposes bootleg as MCP tools for Claude; internal/importer does the work.
package connector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moudlajs/bootleg/internal/applemusic"
	"github.com/moudlajs/bootleg/internal/config"
	"github.com/moudlajs/bootleg/internal/importer"
	"github.com/moudlajs/bootleg/internal/parser"
)

const (
	// maxSongs keeps one call well inside Claude's wait (about 1 s per song).
	maxSongs = 50
	// callTimeout is below Cloud Run's request timeout, so a slow call fails with a clear error.
	callTimeout = 4 * time.Minute
)

// Service is what the tools need: an Apple Music client and how to pace it.
type Service struct {
	API        *applemusic.Client
	Storefront string        // catalog country, e.g. cz
	Delay      time.Duration // pause between requests
	Log        *slog.Logger
	// TokenExpires is the developer token's expiry, zero if unknown.
	TokenExpires time.Time
}

// NewServer returns an MCP server with bootleg's tools registered.
func NewServer(svc *Service, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "bootleg", Version: version}, &sdk.ServerOptions{
		Instructions: "Adds songs to the user's Apple Music: a playlist, the Library, or Favourite Songs. " +
			"Songs are lines of \"Artist - Title\". From a screenshot or a pasted tracklist, transcribe one song per " +
			"line first. Always call preview_songs, show the user what matched and what didn't, and only call " +
			"add_songs once they confirm where the songs should go.",
	})

	sdk.AddTool(s, &sdk.Tool{
		Name: "preview_songs",
		Description: "Look songs up in the Apple Music catalog without changing anything: for each line, the matched " +
			"artist, title and ID, or no match. Karaoke and tribute versions are skipped. Use before add_songs.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)},
	}, handler(svc.Log, "preview_songs", func(ctx context.Context, in SongsInput) (PreviewOutput, error) {
		out, err := svc.preview(ctx, in)
		out.Server = serverInfo(version, svc.TokenExpires, time.Now())
		return out, err
	}))

	sdk.AddTool(s, &sdk.Tool{
		Name: "add_songs",
		Description: "Add songs to the user's Apple Music. to: pl (a playlist: give playlist_name to create one, or " +
			"playlist_id from list_playlists to add to an existing one), lib (the Library), fav (Favourite Songs, " +
			"which also adds to the Library). Several at once, e.g. [\"pl\",\"fav\"]. Songs as lines and/or " +
			"song_ids (e.g. an alternative the user picked). Only after the user confirmed a preview.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: false, OpenWorldHint: ptr(true)},
	}, handler(svc.Log, "add_songs", svc.add))

	sdk.AddTool(s, &sdk.Tool{
		Name: "remove_songs",
		Description: "Undo: remove songs from a playlist (pl, with playlist_id), the Library (lib) and/or Favourite " +
			"Songs (fav). Songs as lines and/or song_ids. This deletes things: say exactly what will be removed and " +
			"from where, and only call it after the user confirms. Songs that weren't there are reported, not errors.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(true)},
	}, handler(svc.Log, "remove_songs", svc.remove))

	sdk.AddTool(s, &sdk.Tool{
		Name:        "list_playlists",
		Description: "The playlists in the user's Apple Music library, with their IDs, so songs can be added to an existing one.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)},
	}, handler(svc.Log, "list_playlists", func(ctx context.Context, in struct{}) (PlaylistsOutput, error) {
		out, err := svc.playlists(ctx, in)
		out.Server = serverInfo(version, svc.TokenExpires, time.Now())
		return out, err
	}))

	return s
}

// SongsInput is preview_songs' arguments.
type SongsInput struct {
	Songs string `json:"songs" jsonschema:"one song per line as Artist - Title (lines without ' - ' are searched as they are; blank lines and lines starting with # are ignored); at most 50"`
}

// AddInput is add_songs' arguments.
type AddInput struct {
	Songs        string   `json:"songs,omitempty" jsonschema:"the same lines as for preview_songs; at most 50 songs in total with song_ids"`
	SongIDs      []string `json:"song_ids,omitempty" jsonschema:"catalog IDs to add as they are, e.g. an alternative the user picked from preview_songs"`
	To           []string `json:"to" jsonschema:"one or more of: pl (playlist), lib (Library), fav (Favourite Songs)"`
	PlaylistName string   `json:"playlist_name,omitempty" jsonschema:"with pl: create a new playlist with this name"`
	PlaylistID   string   `json:"playlist_id,omitempty" jsonschema:"with pl: add to this existing playlist (ID from list_playlists) instead"`
}

// Match is one matched line.
type Match struct {
	Line   int    `json:"line"`
	Query  string `json:"query"`
	Artist string `json:"artist"`
	Title  string `json:"title"`
	ID     string `json:"id"`
}

// Unmatched is a line with no acceptable catalog result.
type Unmatched struct {
	Line         int     `json:"line"`
	Query        string  `json:"query"`
	Alternatives []Match `json:"alternatives,omitempty" jsonschema:"closest catalog results; offer them as 'did you mean' and add a chosen one by song_ids"`
}

// PreviewOutput is preview_songs' result.
type PreviewOutput struct {
	Matched   []Match     `json:"matched"`
	Unmatched []Unmatched `json:"unmatched"`
	Skipped   int         `json:"skipped" jsonschema:"blank and comment lines"`
	Server    ServerInfo  `json:"server"`
}

// PlaylistRef names a playlist.
type PlaylistRef struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Created bool   `json:"created,omitempty"`
	Songs   int    `json:"songs,omitempty" jsonschema:"songs added in this call"`
}

// AddOutput is add_songs' result.
type AddOutput struct {
	Matched        int          `json:"matched"`
	Unmatched      []Unmatched  `json:"unmatched" jsonschema:"lines not added anywhere; suggest a corrected spelling"`
	Playlist       *PlaylistRef `json:"playlist,omitempty"`
	AddedToLibrary int          `json:"added_to_library"`
	Favourited     int          `json:"favourited"`
	Note           string       `json:"note,omitempty" jsonschema:"tell the user this"`
}

const favouritesDelay = "Apple Music has the favourites already, but Favourite Songs on the user's devices can take " +
	"a few minutes to show them (iCloud sync is slower for favourites than for playlists). If they're missing, wait a " +
	"few minutes before trying again: adding them twice changes nothing."

// PlaylistsOutput is list_playlists' result.
type PlaylistsOutput struct {
	Playlists []PlaylistInfo `json:"playlists"`
	Server    ServerInfo     `json:"server"`
}

// ServerInfo lets a chat notice it has an outdated tool list (claude.ai caches it until reconnect).
type ServerInfo struct {
	Version string   `json:"version"`
	Tools   []string `json:"tools" jsonschema:"every tool this server has"`
	Note    string   `json:"note"`
	// TokenExpires is when the Apple Music developer token stops working.
	TokenExpires string `json:"token_expires,omitempty"`
}

// toolNames are every tool NewServer registers; a test keeps it in step.
var toolNames = []string{"preview_songs", "add_songs", "remove_songs", "list_playlists"}

const reconnectNote = "If any of these tools are missing from your tool list, bootleg was updated after this " +
	"connector's tools were loaded: tell the user to reconnect the bootleg connector (claude.ai Settings > " +
	"Connectors) and start a new chat. Don't improvise what a missing tool would do."

func serverInfo(version string, tokenExpires, now time.Time) ServerInfo {
	info := ServerInfo{Version: version, Tools: toolNames, Note: reconnectNote}
	if tokenExpires.IsZero() {
		return info
	}
	info.TokenExpires = tokenExpires.Format(time.DateOnly)
	if tokenExpires.Sub(now) < config.ExpiryWarning {
		info.Note += " Also tell the user: the Apple Music developer token expires on " + info.TokenExpires +
			"; they should get fresh tokens (docs/tokens.md) and run deploy/tokens.sh soon."
	}
	return info
}

// PlaylistInfo is one library playlist.
type PlaylistInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	CanAdd   bool   `json:"can_add" jsonschema:"false for playlists songs can't be added to, like Favourite Songs"`
	Modified string `json:"modified,omitempty"`
}

func (s *Service) preview(ctx context.Context, in SongsInput) (PreviewOutput, error) {
	lines, skipped, err := s.resolve(ctx, in.Songs)
	if err != nil {
		return PreviewOutput{}, err
	}
	out := PreviewOutput{Matched: []Match{}, Unmatched: []Unmatched{}, Skipped: skipped}
	for _, l := range lines {
		if l.OK {
			out.Matched = append(out.Matched, Match{Line: l.Query.Line, Query: l.Query.Raw, Artist: l.Song.Artist, Title: l.Song.Name, ID: l.Song.ID})
		} else {
			u := Unmatched{Line: l.Query.Line, Query: l.Query.Raw}
			for _, a := range l.Alternatives {
				u.Alternatives = append(u.Alternatives, Match{Line: l.Query.Line, Query: l.Query.Raw, Artist: a.Artist, Title: a.Name, ID: a.ID})
			}
			out.Unmatched = append(out.Unmatched, u)
		}
	}
	return out, nil
}

func (s *Service) add(ctx context.Context, in AddInput) (AddOutput, error) {
	opts, err := writeOptions(in, s.Delay)
	if err != nil {
		return AddOutput{}, err
	}
	// Chats retry, so refuse a duplicate name rather than make a second playlist.
	if opts.To.Playlist && opts.PlaylistName != "" {
		if err := s.checkNameFree(ctx, opts.PlaylistName); err != nil {
			return AddOutput{}, err
		}
	}

	byID, err := songIDs(in.SongIDs)
	if err != nil {
		return AddOutput{}, err
	}
	// Fail before spending paced searches on a call that can't fit.
	if len(byID) > maxSongs {
		return AddOutput{}, fmt.Errorf("more than %d songs per call; split the list and send it in parts", maxSongs)
	}
	var lines []importer.Line
	switch {
	case strings.TrimSpace(in.Songs) != "":
		// Lines that are only comments are fine when IDs are given.
		if lines, _, err = s.resolve(ctx, in.Songs); err != nil && (!errors.Is(err, errNoSongs) || len(byID) == 0) {
			return AddOutput{}, err
		}
	case len(byID) == 0:
		return AddOutput{}, errors.New("no songs given: send songs (Artist - Title lines) and/or song_ids")
	}
	ids, unmatched := importer.Split(lines)
	ids = appendNew(ids, byID)
	if len(ids) > maxSongs {
		return AddOutput{}, fmt.Errorf("%d songs is more than %d per call; split the list and send it in parts", len(ids), maxSongs)
	}
	out := AddOutput{Matched: len(ids), Unmatched: []Unmatched{}}
	for _, q := range unmatched {
		out.Unmatched = append(out.Unmatched, Unmatched{Line: q.Line, Query: q.Raw})
	}
	if len(ids) == 0 {
		return out, errors.New("none of the songs matched anything in the catalog, so nothing was added; check the spelling with preview_songs")
	}

	w, err := importer.Write(ctx, s.API, opts, ids, s.Log)
	if w.Playlist > 0 {
		out.Playlist = &PlaylistRef{ID: w.PlaylistID, Created: w.PlaylistCreated, Songs: w.Playlist}
		if w.PlaylistCreated {
			out.Playlist.Name = opts.PlaylistName
		}
	}
	out.AddedToLibrary, out.Favourited = w.Library, w.Favorites
	if w.Favorites > 0 {
		out.Note = favouritesDelay
	}
	if err != nil {
		var inc *importer.IncompleteError
		if errors.As(err, &inc) {
			return out, fmt.Errorf("%s. Already done: %s. To finish, call add_songs again with the same songs and to=[%s]",
				people(inc.Err), strings.Join(inc.Done, ", "), strings.Join(inc.Rest, ", "))
		}
		if errors.Is(err, applemusic.ErrNotFound) && opts.PlaylistID != "" {
			return out, fmt.Errorf("playlist %s isn't in the library; use list_playlists to find the right playlist_id", opts.PlaylistID)
		}
		return out, errors.New(people(err))
	}
	return out, nil
}

func (s *Service) playlists(ctx context.Context, _ struct{}) (PlaylistsOutput, error) {
	pls, err := s.API.ListPlaylists(ctx)
	if err != nil {
		return PlaylistsOutput{}, errors.New(people(err))
	}
	out := PlaylistsOutput{Playlists: make([]PlaylistInfo, 0, len(pls))}
	for _, p := range pls {
		out.Playlists = append(out.Playlists, PlaylistInfo{ID: p.ID, Name: p.Name, CanAdd: p.CanEdit, Modified: p.Modified})
	}
	return out, nil
}

var errNoSongs = errors.New("no songs given: send one song per line as Artist - Title")

// songIDs checks catalog IDs: digits only, so they're safe in paths and queries.
func songIDs(in []string) ([]string, error) {
	var out []string
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || strings.Trim(id, "0123456789") != "" {
			return nil, fmt.Errorf("%q is not a catalog song ID (digits only, as preview_songs returns them)", id)
		}
		out = append(out, id)
	}
	return out, nil
}

// appendNew appends the IDs not already in ids, keeping order.
func appendNew(ids, more []string) []string {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range more {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *Service) resolve(ctx context.Context, songs string) ([]importer.Line, int, error) {
	queries, skipped, err := parser.Parse(strings.NewReader(songs))
	if err != nil {
		return nil, 0, fmt.Errorf("reading the songs: %w", err)
	}
	switch {
	case len(queries) == 0:
		return nil, 0, errNoSongs
	case len(queries) > maxSongs:
		return nil, 0, fmt.Errorf("%d songs is more than %d per call; split the list and send it in parts", len(queries), maxSongs)
	}
	lines, err := importer.Resolve(ctx, s.API, s.Storefront, s.Delay, queries, s.Log)
	if err != nil {
		return nil, 0, errors.New(people(err))
	}
	return lines, skipped, nil
}

func (s *Service) checkNameFree(ctx context.Context, name string) error {
	pls, err := s.API.ListPlaylists(ctx)
	if err != nil {
		return errors.New(people(err))
	}
	for _, p := range pls {
		if strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(name)) {
			return fmt.Errorf("a playlist named %q already exists (id %s). To add these songs to it, call add_songs with playlist_id %q; to make a separate one, ask the user for a different name",
				p.Name, p.ID, p.ID)
		}
	}
	return nil
}

func writeOptions(in AddInput, delay time.Duration) (importer.WriteOptions, error) {
	if len(in.To) == 0 {
		return importer.WriteOptions{}, errors.New("say where the songs go: to = pl, lib and/or fav")
	}
	t, err := importer.ParseTargets(strings.Join(in.To, ","))
	if err != nil {
		return importer.WriteOptions{}, err
	}
	name, id := strings.TrimSpace(in.PlaylistName), strings.TrimSpace(in.PlaylistID)
	switch {
	case name != "" && id != "":
		return importer.WriteOptions{}, errors.New("give either playlist_name (new playlist) or playlist_id (existing one), not both")
	case t.Playlist && name == "" && id == "":
		return importer.WriteOptions{}, errors.New("to pl needs playlist_name for a new playlist or playlist_id (see list_playlists) for an existing one")
	case !t.Playlist && (name != "" || id != ""):
		return importer.WriteOptions{}, errors.New("playlist_name and playlist_id only apply with pl in to")
	}
	return importer.WriteOptions{To: t, PlaylistName: name, PlaylistID: id, Delay: delay}, nil
}

// people words an error for the user; Apple client errors never carry tokens.
func people(err error) string {
	switch {
	case errors.Is(err, applemusic.ErrUnauthorized):
		return "Apple Music rejected the web-player tokens; they have probably expired. The owner needs fresh tokens " +
			"from music.apple.com (DevTools > Network > amp-api request headers) in bootleg's .env, then deploy/tokens.sh " +
			"(docs/tokens.md)"
	case errors.Is(err, applemusic.ErrRateLimited):
		return "Apple Music is rate limiting requests; wait a minute, then try again with fewer songs"
	case errors.Is(err, applemusic.ErrNotFound):
		return "Apple Music answered \"not found\""
	case errors.Is(err, context.DeadlineExceeded):
		return "this took too long and was stopped; try fewer songs per call"
	case errors.Is(err, importer.ErrWriteInterrupted):
		return "the request was cut off while writing; check Apple Music before retrying, the change may have been applied"
	default:
		return err.Error()
	}
}

// handler adds a time limit and one log line (no song names) to a tool function.
func handler[In, Out any](log *slog.Logger, name string, h func(context.Context, In) (Out, error)) sdk.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		start := time.Now()
		out, err := h(ctx, in)
		log.Info("tool call", "tool", name, "ok", err == nil, "ms", time.Since(start).Milliseconds())
		return nil, out, err
	}
}

func ptr[T any](v T) *T { return &v }
