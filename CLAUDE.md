# Notes for Claude

Read `CONTRIBUTING.md` first. The draft → ready → review → merge flow applies
to you too, and it is how this repo guarantees quality.

## What this repository is

`bootleg`: a Go CLI that reads `Artist - Title` lines from a text file and
sends the songs to a playlist, the Library and/or Favourite Songs (`-to`).
It uses the Apple Music web player's tokens and private
`amp-api.music.apple.com` endpoints, not MusicKit, so no paid developer
account is needed. The user supplies the tokens.

The maintainer is experienced in backend/infra but new to Go. Write
idiomatic, boring Go, and explain non-obvious idioms in a short comment.

## The rules

1. **Never leak a token.** `AM_DEV_TOKEN` and `AM_USER_TOKEN` values must not
   appear in logs (even at debug), error messages, test fixtures that look
   real, or commits. Log that a header was set, never its value.
2. **Keep it small.** Sequential requests are intentional. No goroutine
   pools, no CLI framework, no config framework, no interfaces with one
   implementation, no plugin systems. Standard library only in the binary,
   plus `golang.org/x/text` for diacritic folding.
3. **No attribution lines.** No `Co-Authored-By: Claude` trailer and no
   "Generated with Claude Code" footer in commits (including PR-branch
   commits: the squash merge copies their trailers into `main`), PR bodies,
   or review comments. The review gate fails if the `claude[bot]` summary
   has a footer line; manual reviews must not have one either.
4. **No real network in tests.** `httptest.Server` for the client, `t.Setenv`
   for env.

## Architecture

```
cmd/bootleg/main.go   flags, signal.NotifyContext, build deps, call run(), map error → exit code
internal/config         env vars + tiny .env loader (only fills unset vars)
internal/parser         input file → []Query{Artist, Title, Raw}
internal/matcher        PURE: normalise, reject karaoke/tribute, score, pick best
internal/applemusic     HTTP client: Search, CreatePlaylist, AddTracks, AddToLibrary, Favorite
internal/importer       the engine: Resolve (search + match), Write (playlist/library/favourites); no printing, no files
```

- The CLI (and the connector) are thin shells over `internal/importer`:
  they read input, call `Resolve` and `Write`, and word the results. Files
  (`unmatched.txt`) and resume hints belong to the shell, not the engine.

- `main` stays thin: parse flags, build dependencies, call
  `run(ctx, ...) error`, map errors to exit codes.
- The client takes an `*http.Client` and a base URL so tests can point it at
  an `httptest.Server`.
- `internal/matcher` does no I/O and no network. Everything is decided from
  its arguments.
- Errors are wrapped with `fmt.Errorf("...: %w", err)`. The sentinels
  `applemusic.ErrUnauthorized` and `applemusic.ErrRateLimited` are checked in
  `main` with `errors.Is`.
- `context.Context` is the first parameter of anything that does I/O or waits.

## API facts

- Base `https://amp-api.music.apple.com`. Every request sends
  `Authorization: Bearer <dev>`, `Media-User-Token: <user>`,
  `Origin: https://music.apple.com`.
- Search: `GET /v1/catalog/{storefront}/search?term=...&types=songs&limit=5`
- Create: `POST /v1/me/library/playlists` with
  `{"attributes":{"name":...},"relationships":{"tracks":{"data":[{"id":...,"type":"songs"}]}}}`
- Append: `POST /v1/me/library/playlists/{id}/tracks` with `{"data":[...]}`
- Library: `POST /v1/me/library?ids[songs]=a,b` → 202, applied within seconds.
  Undo: `DELETE /v1/me/library/songs/{libraryId}` → 204.
- Favourite: `PUT /v1/me/ratings/songs/{id}` with
  `{"type":"rating","attributes":{"value":1}}` → 200. The song tops the
  automatic "Favourite Songs" playlist and joins the Library, even if it
  wasn't there. Undo: `DELETE` the same path → 204. (`/v1/me/favorites`
  exists but isn't what the app's star uses; don't switch to it.)
- All of the above verified against the real API on 2026-10-07 (UTC).
- 401/403 means the web-player tokens expired (exit 2).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | every line matched and went to every `-to` destination |
| 1 | partial: some lines unmatched (listed in `unmatched.txt`; in `-dry-run` only printed), or any other failure: 429, 5xx, network |
| 2 | auth: tokens missing, expired or rejected |
| 3 | input: bad flags, unreadable file, nothing to import, `-playlist-id` not in the library |

## Things that have already bitten

- **A skipped required check counts as passing.** A push and `gh pr ready`
  two seconds apart once let `review` go green with no review (#16). The
  review workflow now skips a draft only when both the event payload and
  the live API say draft; never move that back into the job-level `if`.
  Dependabot PRs are the one deliberate skip (no secrets), gated by CI only.
- **claude-code-action can exit green without reviewing.** It refuses to
  run on a PR that changes its own workflow file, and says so only in an
  annotation (#17). The last step of `claude-review.yml` fails the job
  unless `claude[bot]` posted a `## Claude review of <head sha>` summary.
- **PRs that change `claude-review.yml` cannot be reviewed by the action.**
  Keep them to that file plus top-level `*.md` (the gate fails otherwise).
  Before merging, run an independent review with a fresh subagent that did
  not write the change, post it with `gh pr comment` with the first line
  exactly `## Independent review (manual - <full head sha>)`, address it,
  then re-run the `review` job. Any new push needs a new manual review.
- **zsh heredocs expand `\uXXXX`.** Writing Go source through
  `cat <<'EOF'` turned the `\uFEFF` escape into a literal BOM, which Go
  rejects. Write Go files with an editor tool, not a heredoc.

## Commands

```sh
make fmt           # gofmt -w
make lint          # gofmt check, go vet, golangci-lint
make test          # go test ./... -race -timeout 2m -coverprofile=coverage.out
make build         # bin/bootleg with version from git describe
```

## Pull requests

```sh
git switch -c feat/<thing>
make lint test
gh pr create --draft --title "feat(scope): ..." --body "Closes #N ..."
# ...iterate until CI is green and it is actually done...
gh pr ready <n>    # fires the independent Claude review
```

Answer every review thread, then resolve it:

```sh
gh api graphql -f query='{ repository(owner:"moudlajs", name:"bootleg") {
  pullRequest(number:N) { reviewThreads(first:50) {
    nodes { id isResolved path line comments(first:1){nodes{body}} } } } } }'
gh api graphql -f query='mutation { addPullRequestReviewThreadReply(
  input:{pullRequestReviewThreadId:"ID", body:"..."}) { comment { id } } }'
gh api graphql -f query='mutation { resolveReviewThread(
  input:{threadId:"ID"}) { thread { isResolved } } }'
```

Merge with `gh pr merge <n> --squash` only when all checks are green and every
thread is resolved.
