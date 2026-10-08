#!/usr/bin/env bash
# New connector sign-in passphrase: stored in Secret Manager, copied to the clipboard and the login Keychain, never printed.
# Already-connected clients stay signed in (their tokens use the signing key).
#   deploy/passphrase.sh [project-id]
set -euo pipefail
PROJECT=${1:-bootleg-638112}
REGION=europe-west1
SERVICE=bootleg

command -v pbcopy >/dev/null || { echo "needs macOS pbcopy (the passphrase is never printed)" >&2; exit 1; }

# 4x5 chars, no 0/o/1/l, ~100 bits; `|| true` because head SIGPIPEs tr, which pipefail would treat as failure.
pass=$( (LC_ALL=C tr -dc 'abcdefghijkmnpqrstuvwxyz23456789' </dev/urandom || true) | head -c 20 |
  sed -E 's/(.{5})(.{5})(.{5})(.{5})/\1-\2-\3-\4/')
[ ${#pass} -eq 23 ] || { echo "could not generate a passphrase" >&2; exit 1; }

printf '%s' "$pass" | gcloud --project "$PROJECT" --quiet secrets versions add bootleg-passphrase --data-file=- >/dev/null
printf '%s' "$pass" | pbcopy
# Via stdin to `security -i` so the passphrase never appears in argv.
printf 'add-generic-password -U -a %s -s "bootleg connector passphrase" -w %s\n' "$USER" "$pass" | security -i >/dev/null
unset pass

if gcloud --project "$PROJECT" run services describe "$SERVICE" --region "$REGION" >/dev/null 2>&1; then
  gcloud --project "$PROJECT" --quiet run services update "$SERVICE" --region "$REGION" \
    --update-env-vars "PASSPHRASE_UPDATED=$(date -u +%Y%m%dT%H%M%SZ)" >/dev/null
  echo "Rolled a new revision of $SERVICE."
fi
echo "New passphrase stored, copied to your clipboard and saved in your Keychain"
echo "(security find-generic-password -s \"bootleg connector passphrase\" -w)."
