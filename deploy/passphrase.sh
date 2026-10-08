#!/usr/bin/env bash
# Generates a new sign-in passphrase for the connector, stores it in Secret
# Manager, copies it to the clipboard and keeps it in the macOS login
# Keychain. It is never printed. Anyone with it can change your Apple Music.
#
# Changing it doesn't sign out clients that are already connected (their
# tokens are signed by the signing key, not the passphrase).
#
#   deploy/passphrase.sh [project-id]
set -euo pipefail
PROJECT=${1:-waiverwatch-509716}
REGION=europe-west1
SERVICE=bootleg

command -v pbcopy >/dev/null || { echo "needs macOS pbcopy (the passphrase is never printed)" >&2; exit 1; }

# Four groups of five from an unambiguous alphabet (no 0/o, 1/l): easy to
# type on a phone, about 100 bits.
# (tr is cut off by head with SIGPIPE; that's expected, so it may "fail"
# without failing the script under pipefail.)
pass=$( (LC_ALL=C tr -dc 'abcdefghijkmnpqrstuvwxyz23456789' </dev/urandom || true) | head -c 20 |
  sed -E 's/(.{5})(.{5})(.{5})(.{5})/\1-\2-\3-\4/')
[ ${#pass} -eq 23 ] || { echo "could not generate a passphrase" >&2; exit 1; }

printf '%s' "$pass" | gcloud --project "$PROJECT" --quiet secrets versions add bootleg-passphrase --data-file=- >/dev/null
printf '%s' "$pass" | pbcopy
# Also kept in the login Keychain, so a clobbered clipboard isn't a lockout:
#   security find-generic-password -s "bootleg connector passphrase" -w
# Sent on stdin to `security -i`, not as an argument, so it never shows up
# in the process list. (The alphabet needs no quoting.)
printf 'add-generic-password -U -a %s -s "bootleg connector passphrase" -w %s\n' "$USER" "$pass" | security -i >/dev/null
unset pass

if gcloud --project "$PROJECT" run services describe "$SERVICE" --region "$REGION" >/dev/null 2>&1; then
  gcloud --project "$PROJECT" --quiet run services update "$SERVICE" --region "$REGION" \
    --update-env-vars "PASSPHRASE_UPDATED=$(date -u +%Y%m%dT%H%M%SZ)" >/dev/null
  echo "Rolled a new revision of $SERVICE."
fi
echo "New passphrase stored, copied to your clipboard and saved in your Keychain"
echo "(security find-generic-password -s \"bootleg connector passphrase\" -w)."
