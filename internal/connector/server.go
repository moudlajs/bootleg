// Package connector exposes bootleg as MCP tools for Claude, so it can be
// used from the Claude apps (phone included) as a connector. It knows MCP
// and how to word results for people; the work is done by
// internal/importer.
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
	"github.com/moudlajs/bootleg/internal/importer"
	"github.com/moudlajs/bootleg/internal/parser"
)

const (
	// maxSongs per call keeps one tool call well inside the time Claude
	// waits for it (about 1 s per song including the pause).
	maxSongs = 50
	// callTimeout bounds one tool call; Cloud Run's request timeout is set
	// higher, so the call fails with a clear error rather than a cut-off.
	callTimeout = 4 * time.Minute
)

// Service is what the tools need: an Apple Music client and how to pace it.
type Service struct {
	API        *applemusic.Client
	Storefront string        // catalog country, e.g. cz
	Delay      time.Duration // pause between requests
	Log        *slog.Logger
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
	}, handler(svc.Log, "preview_songs", svc.preview))

	sdk.AddTool(s, &sdk.Tool{
		Name: "add_songs",
		Description: "Add songs to the user's Apple Music. to: pl (a playlist: give playlist_name to create one, or " +
			"playlist_id from list_playlists to add to an existing one), lib (the Library), fav (Favourite Songs, " +
			"which also adds to the Library). Several at once, e.g. [\"pl\",\"fav\"]. Only after the user confirmed a preview.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: false, OpenWorldHint: ptr(true)},
	}, handler(svc.Log, "add_songs", svc.add))

	sdk.AddTool(s, &sdk.Tool{
		Name:        "list_playlists",
		Description: "The playlists in the user's Apple Music library, with their IDs, so songs can be added to an existing one.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)},
	}, handler(svc.Log, "list_playlists", svc.playlists))

	return s
}

// SongsInput is preview_songs' arguments.
type SongsInput struct {
	Songs string `json:"songs" jsonschema:"one song per line as Artist - Title (lines without ' - ' are searched as they are; blank lines and lines starting with # are ignored); at most 50"`
}

// AddInput is add_songs' arguments.
type AddInput struct {
	Songs        string   `json:"songs" jsonschema:"the same lines as for preview_songs; at most 50"`
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
	Line  int    `json:"line"`
	Query string `json:"query"`
}

// PreviewOutput is preview_songs' result.
type PreviewOutput struct {
	Matched   []Match     `json:"matched"`
	Unmatched []Unmatched `json:"unmatched"`
	Skipped   int         `json:"skipped" jsonschema:"blank and comment lines"`
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

// favouritesDelay is said whenever songs were favourited: Apple has them
// at once, but the apps only show new favourites after iCloud syncs, which
// took about five minutes in the owner's test (#55).
const favouritesDelay = "Apple Music has the favourites already, but Favourite Songs on the user's devices can take " +
	"a few minutes to show them (iCloud sync is slower for favourites than for playlists). If they're missing, wait a " +
	"few minutes before trying again: adding them twice changes nothing."

// PlaylistsOutput is list_playlists' result.
type PlaylistsOutput struct {
	Playlists []PlaylistInfo `json:"playlists"`
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
			out.Unmatched = append(out.Unmatched, Unmatched{Line: l.Query.Line, Query: l.Query.Raw})
		}
	}
	return out, nil
}

func (s *Service) add(ctx context.Context, in AddInput) (AddOutput, error) {
	opts, err := writeOptions(in, s.Delay)
	if err != nil {
		return AddOutput{}, err
	}
	// Chats retry: a second "create Road trip" would make a second playlist
	// with the same name. Refuse and point at the existing one instead.
	if opts.To.Playlist && opts.PlaylistName != "" {
		if err := s.checkNameFree(ctx, opts.PlaylistName); err != nil {
			return AddOutput{}, err
		}
	}

	lines, _, err := s.resolve(ctx, in.Songs)
	if err != nil {
		return AddOutput{}, err
	}
	ids, unmatched := importer.Split(lines)
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

// resolve parses the song lines and looks each one up.
func (s *Service) resolve(ctx context.Context, songs string) ([]importer.Line, int, error) {
	queries, skipped, err := parser.Parse(strings.NewReader(songs))
	if err != nil {
		return nil, 0, fmt.Errorf("reading the songs: %w", err)
	}
	switch {
	case len(queries) == 0:
		return nil, 0, errors.New("no songs given: send one song per line as Artist - Title")
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

// writeOptions checks add_songs' arguments the way the CLI checks its
// flags.
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

// people words an error for the user. Apple client errors carry method,
// path and status only, never tokens.
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

// handler adds a time limit and one log line per call to a tool function.
// Song names are logged only as a count.
// (A function, not a method: Go methods can't have type parameters.)
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
