# Hosting the connector

The connector (`cmd/bootleg-mcp`) runs on Google Cloud Run in its own
project, and every release deploys it.

## One-time setup

```sh
deploy/setup.sh <project-id> <billing-account-id>
deploy/passphrase.sh
deploy/tokens.sh
```

- **setup.sh** (safe to re-run) creates the image registry, a runtime
  account that can read only bootleg's secrets, a deploy account GitHub
  Actions signs in as without keys (only from `main` and `v*` tags), the
  secrets, a monthly budget with a billing kill switch, and the repository
  variables the deploy needs.
- **passphrase.sh** creates the sign-in passphrase, stores it in Secret
  Manager, copies it to your clipboard and saves it in the macOS Keychain.
  It's never printed. To find it again:

  ```sh
  security find-generic-password -s "bootleg connector passphrase" -w
  ```

- **tokens.sh** copies your Apple Music tokens from `.env` into Secret
  Manager ([tokens.md](tokens.md)).

Then tag a release (`vX.Y.Z`): the release workflow publishes the command
line binaries and deploys the connector, checking that it's healthy and
refuses requests without sign-in.

## Day to day

- **Tokens expired:** update `.env`, run `deploy/tokens.sh`.
- **New passphrase:** run `deploy/passphrase.sh`. Connected clients stay
  signed in.
- **Redeploy or roll back:** run the Deploy workflow by hand with a tag.

## Cost

One instance at most, scaled to zero when idle, so normal use stays in the
free tier. The budget alert and kill switch stop billing for the project if
it ever reaches the budget.
