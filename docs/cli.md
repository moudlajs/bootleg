# Command line

```text
bootleg [-to pl,lib,fav] [-name "Playlist" | -playlist-id ID] [-storefront cz] [-dry-run] [-delay 500ms] [-v] <file.txt>
```

## Install

With Go 1.26 or newer:

```sh
go install github.com/moudlajs/bootleg/cmd/bootleg@latest
```

Or download a binary for macOS or Linux (amd64/arm64) from
[Releases](https://github.com/moudlajs/bootleg/releases) and check it:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf bootleg_*_darwin_arm64.tar.gz
./bootleg -version
```

You'll also need your tokens: [tokens.md](tokens.md).

## Where songs go: `-to`

| `-to` | Destination | Notes |
|---|---|---|
| `pl` (default) | a playlist | needs `-name` (new) or `-playlist-id` (existing) |
| `lib` | your Library | |
| `fav` | Favourite Songs | also adds to your Library, as in the app |

Combine them with commas, e.g. `-to pl,fav`. They're written in the order
playlist, Library, favourites. If a step fails, the error says what's
already done and the exact command to finish, without creating the
playlist twice.

## Flags

| Flag | Meaning |
|---|---|
| `-to` | Destinations, see above. Default `pl`. |
| `-name` | Name of a new playlist (with `pl`). |
| `-playlist-id` | Add to this existing playlist instead (with `pl`). |
| `-dry-run` | Show the matches, change nothing. |
| `-storefront` | Catalog country, e.g. `cz`. Default `$AM_STOREFRONT`, else `us`. |
| `-delay` | Pause between requests. Default `500ms`. |
| `-v` | Detailed log on stderr. |
| `-version` | Print the version. |

## Input

One song per line, `Artist - Title`:

- Only the first ` - ` (with spaces) splits, so `Queen - Bohemian Rhapsody - Remastered`
  and `Jay-Z - 99 Problems` both work.
- A line without ` - ` is searched as it is.
- Blank lines and lines starting with `#` are skipped.
- UTF-8, with or without a BOM; LF or CRLF.

Matching ignores case, accents and punctuation (`Bjork` finds `Björk`). It
skips karaoke, tribute, "made famous by" and "in the style of" versions
unless your line asks for them, and needs both artist and title to match.

## Examples

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

```console
$ bootleg -name "Road trip" roadtrip.txt
Matched 4, unmatched 1, skipped 1 (blank or comment).
Unmatched:
  line 6: The Nonexistents - Imaginary Song
Written to unmatched.txt - fix the lines and feed it back in.
Created "Road trip" with 4 songs (playlist ID p.Qx9Lm2Kd).
```

Fix `unmatched.txt` and add the rest to the same playlist:

```sh
bootleg -playlist-id p.Qx9Lm2Kd unmatched.txt
```

A playlist's ID is also the last part of its URL on music.apple.com
(`…/library/playlist/p.XXXXXXX`).

`unmatched.txt` is written next to the input file, never in `-dry-run`. A
run where everything matches removes an old one, and the input file itself
is never overwritten.

<kbd>Ctrl</kbd>+<kbd>C</kbd> stops at any point. During the searches nothing
has changed yet; during the final write Apple may already have applied it,
and bootleg says so.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Everything matched and was added. |
| 1 | Some lines didn't match (see the summary and `unmatched.txt`), or another failure such as rate limiting or the network. |
| 2 | The tokens are missing or were rejected: refresh them ([tokens.md](tokens.md)). |
| 3 | Bad flags, a missing or empty file, nothing matched, or a `-playlist-id` that isn't in your library. |
