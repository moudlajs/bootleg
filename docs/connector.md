# Using bootleg from Claude

bootleg runs as a Claude connector, so you can use it from the Claude app on
your phone, on the web or on desktop.

## Connecting

1. Claude → Settings → Connectors → **Add custom connector**.
2. Name: `bootleg`. URL: your connector's address, ending in `/mcp`.
3. **Connect**. A bootleg sign-in page opens; enter your passphrase.
   Connectors added on claude.ai also appear in the phone app.

The connector is for its owner only: it holds your Apple Music session, so
every request needs that sign-in.

## What to ask

- *"Add these to my favourites:"* followed by a list
- A screenshot of a tracklist and *"Make a playlist called Gym from this"*
- *"Put Massive Attack - Teardrop in my Road trip playlist"*
- *"Add these to my library:"* followed by a list
- *"Add Takk… by Sigur Rós to my favourites"*: a whole album
- *"Remove those from my favourites again"*: undo

Lists can be messy: links, bullets, lowercase names. Claude turns them into
one song per line first.

## How it behaves

- Claude looks the songs up first and shows what matched and what didn't.
  For an unclear request it asks before adding.
- Up to 50 songs per request; Claude splits longer lists.
- It adds to an existing playlist when you name one, and never creates a
  second playlist with a name you already have.
- When a song isn't found, Claude suggests the closest matches ("did you
  mean…?") and adds the one you pick.
- Removing asks for confirmation first; songs that weren't there are just
  reported.
- From two weeks before your developer token expires, Claude warns you.
- **Favourites take a few minutes to appear** on your devices (iCloud syncs
  them more slowly than playlists). Apple has them right away; adding them
  again changes nothing.

## After an update

Claude keeps a connector's tool list until you reconnect it. Ordinary
releases don't need that; when tools change, Claude notices and asks you to
reconnect (Settings → Connectors) and start a new chat.

## Tools

| Tool | Does |
|---|---|
| `preview_songs` | Looks songs up, with suggestions for misses; changes nothing |
| `add_songs` | Adds to a playlist (`pl`), the Library (`lib`) and/or Favourite Songs (`fav`) |
| `remove_songs` | Removes from those again |
| `album_tracks` | Finds an album and lists its tracks |
| `list_playlists` | Your playlists and their IDs |

## Running it locally

`bootleg-mcp` also works with Claude Code or Claude Desktop on your own
machine, using your `.env`:

```sh
go install github.com/moudlajs/bootleg/cmd/bootleg-mcp@latest
claude mcp add bootleg -- bootleg-mcp   # run from the folder with your .env
```
