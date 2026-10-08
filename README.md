# bootleg

[![CI](https://github.com/moudlajs/bootleg/actions/workflows/ci.yml/badge.svg)](https://github.com/moudlajs/bootleg/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/moudlajs/bootleg)](https://github.com/moudlajs/bootleg/releases)

Add songs to Apple Music from a list or a screenshot, from Claude on your
phone or from the terminal.

- Send a tracklist or a screenshot to Claude and say where it goes: a
  playlist, your Library or Favourite Songs.
- Or give the command line a text file with one `Artist - Title` per line.
- It finds the original recordings (not karaoke or tribute versions), shows
  you what matched, and tells you what didn't.

## From Claude

1. In Claude: Settings → Connectors → **Add custom connector**, with your
   connector URL (for example `https://bootleg-….run.app/mcp`).
2. Sign in with your passphrase.
3. Ask, for example: *"Make a playlist called Road trip from this"* with a
   screenshot, or *"Add these to my favourites:"* with a list.

Details: [docs/connector.md](docs/connector.md). Hosting your own:
[docs/hosting.md](docs/hosting.md).

## From the terminal

```sh
go install github.com/moudlajs/bootleg/cmd/bootleg@latest

bootleg -dry-run songs.txt                 # preview the matches
bootleg -name "Road trip" songs.txt        # new playlist
bootleg -to fav songs.txt                  # Favourite Songs
```

Details: [docs/cli.md](docs/cli.md).

## Setup

bootleg uses your Apple Music web session: two values you copy once from
music.apple.com into a `.env` file. No developer account is needed.
Step by step: [docs/tokens.md](docs/tokens.md).

## Good to know

bootleg talks to the same service the Apple Music web player uses, with your
own session. That interface isn't a public API, so it may change. bootleg only
adds catalog songs to your own library; it doesn't download or share music.
For personal use. Not affiliated with Apple.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). `make lint test` runs what CI runs.

## License

[MIT](LICENSE)
