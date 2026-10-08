#!/usr/bin/env bash
# Copies the Apple Music web-player tokens from .env into Secret Manager and
# rolls a new revision of the connector so it uses them. Run it whenever
# Apple rejects the tokens (bootleg exits 2, or the connector says they
# expired): refresh .env first (README: Token setup).
#
# Values are piped straight into gcloud and never printed.
#
#   deploy/tokens.sh [path/to/.env] [project-id]
set -euo pipefail
ENV_FILE=${1:-.env}
PROJECT=${2:-bootleg-638112}
REGION=europe-west1
SERVICE=bootleg

[ -f "$ENV_FILE" ] || { echo "no $ENV_FILE: copy .env.example and fill it in (README: Token setup)" >&2; exit 1; }

# value KEY prints KEY's value from ENV_FILE (last one wins), without
# surrounding quotes or a trailing CR. Only ever piped, never echoed.
value() {
  # tr, not sed, drops CRs: \r in a sed pattern isn't portable.
  grep -E "^[[:space:]]*(export[[:space:]]+)?$1[[:space:]]*=" "$ENV_FILE" | tail -n1 | tr -d '\r' |
    sed -E "s/^[^=]*=[[:space:]]*//; s/[[:space:]]*$//; s/^\"(.*)\"$/\1/; s/^'(.*)'$/\1/"
}

for pair in AM_DEV_TOKEN:bootleg-am-dev-token AM_USER_TOKEN:bootleg-am-user-token AM_STOREFRONT:bootleg-am-storefront; do
  key=${pair%%:*} secret=${pair#*:}
  if [ -z "$(value "$key")" ]; then
    if [ "$key" = AM_STOREFRONT ]; then
      printf 'us' | gcloud --project "$PROJECT" --quiet secrets versions add "$secret" --data-file=- >/dev/null
      echo "  $key not set in $ENV_FILE: using us"
      continue
    fi
    echo "$key is not set in $ENV_FILE" >&2
    exit 1
  fi
  value "$key" | tr -d '\n' | gcloud --project "$PROJECT" --quiet secrets versions add "$secret" --data-file=- >/dev/null
  echo "  updated $secret"
done

# Secrets are read when an instance starts, so roll a new revision. Before
# the first deploy there is nothing to roll; the deploy picks them up.
if gcloud --project "$PROJECT" run services describe "$SERVICE" --region "$REGION" >/dev/null 2>&1; then
  gcloud --project "$PROJECT" --quiet run services update "$SERVICE" --region "$REGION" \
    --update-env-vars "TOKENS_UPDATED=$(date -u +%Y%m%dT%H%M%SZ)" >/dev/null
  echo "  rolled a new revision of $SERVICE"
fi
echo "Done."
