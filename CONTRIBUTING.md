# Contributing

Small tool, one maintainer. The conventions exist so that quality is checked
by something other than the author, not to add ceremony.

## The flow

Every change follows the same path:

1. **Issue first.** It says what is wrong or missing and why it matters,
   with acceptance criteria. Typo fixes are exempt.
2. **Branch** `<type>/<short-description>`, lowercase: `feat/parser`,
   `fix/karaoke-filter`. CI rejects other names.
3. **Open a draft PR** as soon as there is something to push:
   `gh pr create --draft`. CI runs on every push; the reviewer does not.
4. **Mark it ready when it is actually done:** `gh pr ready <n>`. This
   triggers the automated Claude review, an independent instance that did
   not write the code. A PR that is not a draft gets reviewed on every
   push, so do not open one non-draft.
5. **Answer every review comment and resolve the thread.** One of:
   - *Fixed*: say what changed and in which commit.
   - *Not fixing*: say why.
   - *Later*: open an issue and link it.

   Branch protection requires all conversations resolved before merging.
6. **Squash merge** once `check`, `conventions` and `review` are green. The
   PR title becomes the commit subject on `main`.

## Commits and PR titles

[Conventional Commits](https://www.conventionalcommits.org/):
`<type>(<scope>): <subject>`, with the *why* in the body.

| Type | For |
|---|---|
| `feat` | new capability |
| `fix` | corrected behaviour |
| `test` | tests only |
| `docs` | README, CLAUDE.md, help text |
| `ci` | workflows |
| `refactor` | no behaviour change |
| `chore` | deps, repo plumbing, releases |

No attribution trailers or "Generated with" footers, in branch commits
too: a squash merge copies branch-commit trailers into `main`.

Scopes: `parser`, `matcher`, `applemusic`, `config`, `cli`, `release`.

## What CI enforces

- `gofmt`, `go vet`, `golangci-lint` (see `.golangci.yml`), zero findings
- `go test -race` on Go 1.26 and 1.27 (the releases Go still supports), `go build`
- `.goreleaser.yml` is valid
- PR title and branch name conventions
- Claude review ran (on non-draft PRs)

## Testing

```sh
make lint test
```

Tests are table-driven, never touch the network (use `httptest.Server`),
never need real tokens, and use `t.Setenv` rather than `os.Setenv`. If you fix
a bug, add the test case that would have caught it.

## Releases

Tag `vX.Y.Z` on `main` and push the tag. The release workflow runs the tests,
then GoReleaser builds darwin/linux × amd64/arm64 with checksums and a GitHub
Release with auto-generated notes.
