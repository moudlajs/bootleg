# Tokens

bootleg signs in to Apple Music with the same two values the web player
uses while you're signed in. You copy them once; bootleg never signs in or
fetches them itself.

## Getting them

1. Open <https://music.apple.com> and sign in.
2. Open the browser's developer tools (<kbd>Cmd</kbd>+<kbd>Opt</kbd>+<kbd>I</kbd>,
   or <kbd>F12</kbd>) and select the **Network** tab.
3. Type `amp-api` in the filter box, then open your Library so a few
   requests appear.
4. Click one of them, open **Headers → Request Headers**, and copy:

   | Request header | Variable | Notes |
   |---|---|---|
   | `authorization` | `AM_DEV_TOKEN` | With or without the `Bearer` prefix. Lasts months. |
   | `media-user-token` | `AM_USER_TOKEN` | Tied to your session; this is the one that expires. |

5. Put them in `.env`:

   ```sh
   cp .env.example .env   # then paste the two values in
   ```

   or export them in your shell (shell variables win over `.env`).

`AM_STOREFRONT` (optional) is your country's catalog, e.g. `cz`, `us`, `gb`.
It defaults to `us`.

Treat the tokens like a password: they give access to your Apple Music
library. `.env` is never committed, and bootleg never prints or logs them.

## Refreshing them

When Apple rejects the tokens, the command line exits with code 2 and Claude
tells you. Repeat the steps above; usually only `AM_USER_TOKEN` changes.
For the hosted connector, then run:

```sh
deploy/tokens.sh
```

It copies them into Secret Manager (without printing them) and restarts the
connector. Nothing changes in Claude.
