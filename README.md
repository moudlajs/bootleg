# bootleg

[![CI](https://github.com/moudlajs/bootleg/actions/workflows/ci.yml/badge.svg)](https://github.com/moudlajs/bootleg/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/moudlajs/bootleg)](https://github.com/moudlajs/bootleg/releases)

Turn a plain text file of songs into an Apple Music library playlist,
through the web player's back door.

A bootleg is music that didn't come through the label's official channels.
This gets music into your library without Apple's official channel: no
MusicKit, no paid developer account, just the tokens your browser already has.

> Formerly `am-import`. The old repo URL redirects here, and v0.1.0 remains
> installable under the old module path.

```text
# road trip
Björk - Army of Me
Portishead - Glory Box
Massive Attack - Teardrop
Sigur Ros - Hoppipolla
The Nonexistents - Imaginary Song
```

saved as `roadtrip.txt`, then:

```sh
bootleg -name "Road trip" roadtrip.txt
```

It searches the Apple Music catalog for each line, picks the best match (the
original recording, not the karaoke or tribute version), and creates the
playlist in one request. Lines it can't match go to `unmatched.txt` so you can
fix them and run again.

## Why no Apple Developer account?

The official route (MusicKit) needs a paid Apple Developer membership to mint a
developer token. The Apple Music web player at music.apple.com already has one,
and it has your user token while you are signed in. `bootleg` reuses those two
tokens and calls the same endpoints the web player calls. You supply the
tokens; the tool never logs in, scrapes or obtains them itself.

## Disclaimer

- These are **undocumented, private endpoints**. Apple can change or remove
  them at any time, and this tool will break when they do.
- It is meant for **personal use with your own account**. Don't use it to
  automate anyone else's library, and don't hammer the API: searches are
  deliberately sequential with a pause between them.
- Despite the name, it doesn't download, copy or share any music. It only
  adds catalog songs you can already stream to your own library.
- Not affiliated with or endorsed by Apple.

## Install

With Go 1.26 or newer:

```sh
go install github.com/moudlajs/bootleg/cmd/bootleg@latest
```

Or download a binary for macOS or Linux (amd64/arm64) from
[Releases](https://github.com/moudlajs/bootleg/releases), and check it
against `checksums.txt`:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf bootleg_*_darwin_arm64.tar.gz
./bootleg -version
```

## Token setup

You need two values from the web player. Both go in environment variables.

1. Open <https://music.apple.com> and sign in.
2. Open DevTools (<kbd>Cmd</kbd>+<kbd>Opt</kbd>+<kbd>I</kbd>, or
   <kbd>F12</kbd>) and select the **Network** tab.
3. Type `amp-api` in the filter box, then click around (open your Library) so
   requests to `amp-api.music.apple.com` appear.
4. Click one of them, then **Headers → Request Headers**, and copy:

   | Request header | Variable | Notes |
   |---|---|---|
   | `authorization` | `AM_DEV_TOKEN` | Drop the `Bearer ` prefix (it's accepted either way). Lasts months. |
   | `media-user-token` | `AM_USER_TOKEN` | Tied to your session. This is the one that expires. |

5. Put them in a `.env` file in the directory you run `bootleg` from:

   ```sh
   cp .env.example .env   # then paste the two values in
   ```

   or export them in your shell. Shell variables win over `.env`.

`AM_STOREFRONT` (optional) sets your country's catalog, e.g. `cz`, `us`, `gb`.
It defaults to `us`; `-storefront` overrides it.

The tokens give access to your Apple Music library, so treat them like a
password. `.env` is gitignored, and `bootleg` never prints or logs them.

**When `bootleg` exits with code 2**, Apple rejected the tokens. Repeat the
steps above; usually only `AM_USER_TOKEN` needs replacing.

## Usage

```text
bootleg [-to pl,lib,fav] [-name "Playlist" | -playlist-id ID] [-storefront cz] [-dry-run] [-delay 500ms] [-v] <file.txt>
```

### Where the songs go: `-to`

Apple Music has three places to collect songs. `-to` picks one or more,
comma-separated:

| `-to` | Destination | Notes |
|---|---|---|
| `pl` (default) | a **playlist** | needs `-name` (create) or `-playlist-id` (append) |
| `lib` | your **Library** ("Songs") | no playlist, just your collection |
| `fav` | **Favourite Songs** (the ⭐) | starring a song also puts it in your Library, as in the app |

```sh
bootleg -name "Road trip" roadtrip.txt        # playlist (default)
bootleg -to lib roadtrip.txt                  # Library only
bootleg -to fav roadtrip.txt                  # star them
bootleg -to pl,fav -name "Road trip" x.txt    # playlist and star them
```

Long forms work too (`playlist`, `library`, `favorites`/`favourites`).
Destinations are written in the order playlist, Library, favourites;
favouriting is one request per song, paced by `-delay`. If one step fails,
the error says what was already done and the exact command to finish
without creating the playlist twice.

| Flag | Meaning |
|---|---|
| `-to` | Destinations: `pl`, `lib`, `fav`, or a comma list. Default `pl`. |
| `-name` | Name of the playlist to create (with `-to pl`). |
| `-playlist-id` | Append to this existing library playlist instead of creating one (with `-to pl`). Use either this or `-name`. |
| `-dry-run` | Search and show the matches, create nothing. |
| `-storefront` | Catalog country, e.g. `cz`. Default `$AM_STOREFRONT`, else `us`. |
| `-delay` | Pause between requests (searches, favourites). Default `500ms`. Raise it if you hit rate limits. |
| `-v` | Debug logging on stderr. |
| `-version` | Print the version. |

### Input format

One song per line, `Artist - Title`:

- Only the **first** ` - ` (with spaces) splits, so `Queen - Bohemian Rhapsody - Remastered` works, and so does `Jay-Z - 99 Problems`.
- A line without ` - ` is searched as-is.
- Blank lines and lines starting with `#` are ignored.
- UTF-8, with or without a BOM, and LF or CRLF line endings.

Matching ignores case, accents and punctuation (`Bjork` finds `Björk`,
`Sigur Ros` finds `Sigur Rós`). It rejects karaoke, tribute, "made famous by"
and "in the style of" results unless your line asks for them, and requires both
the artist and the title to match.

### Examples

Check the matches first:

```console
$ bootleg -dry-run roadtrip.txt
LINE  QUERY                              MATCH                      ID
2     Björk - Army of Me                 Björk - Army of Me         1440833098
3     Portishead - Glory Box             Portishead - Glory Box     1440764786
4     Massive Attack - Teardrop          Massive Attack - Teardrop  1440799025
5     Sigur Ros - Hoppipolla             Sigur Rós - Hoppípolla     1443163367
6     The Nonexistents - Imaginary Song  (no match)                 -

Matched 4, unmatched 1, skipped 1 (blank or comment).
Unmatched:
  line 6: The Nonexistents - Imaginary Song
```

Create the playlist:

```console
$ bootleg -name "Road trip" roadtrip.txt
Matched 4, unmatched 1, skipped 1 (blank or comment).
Unmatched:
  line 6: The Nonexistents - Imaginary Song
Written to unmatched.txt - fix the lines and feed it back in.
Created "Road trip" with 4 songs (playlist ID p.Qx9Lm2Kd).
```

Fix the lines in `unmatched.txt` and add them to the same playlist. The ID is
the last part of the playlist's URL in the web player,
`music.apple.com/library/playlist/p.XXXXXXX`:

```sh
bootleg -playlist-id p.XXXXXXX unmatched.txt
```

Slow down, and see every search and match:

```sh
bootleg -v -delay 2s -name "Big list" big.txt
```

Press <kbd>Ctrl</kbd>+<kbd>C</kbd> at any point to stop. If you stop during
the searches, nothing is created or changed. If you stop during the final
create or append request, Apple may already have applied it; `bootleg` says
so, and you should check your library.

## Exit codes

| Code | Meaning | What to do |
|---|---|---|
| 0 | Every line matched and went to every destination in `-to`. | Nothing. |
| 1 | Partial: some lines didn't match (listed in the summary and in `unmatched.txt`). Also used for other failures: rate limiting, server or network errors, Ctrl+C. | Fix `unmatched.txt` and re-run with `-playlist-id`, or read the error. |
| 2 | Auth: a token is missing, expired or rejected. | Refresh the tokens (see [Token setup](#token-setup)). |
| 3 | Input: bad flags (including an unknown `-to`, or `-name` without `pl`), a missing or empty file, nothing matched at all, or a `-playlist-id` that isn't in your library. | Check the command and the file. |

`unmatched.txt` is written next to the input file. It is never written in
`-dry-run`, and a run that matches everything removes a stale one. If the input
itself is `unmatched.txt`, it is never overwritten or removed.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). `make lint test` runs what CI runs.

## License

[MIT](LICENSE)
