#!/usr/bin/env bash
# One-time (and safely re-runnable) Google Cloud setup for the bootleg
# connector, in the existing waiverwatch project: its billing, budget alert
# and billing kill switch cover bootleg too.
#
# Creates what the deploy workflow needs, with no keys stored anywhere:
# GitHub Actions signs in through Workload Identity Federation, only from
# this repository's main branch or v* tags. Also creates the token signing
# key and the empty secrets, readable only by bootleg's runtime account.
# After it, run deploy/passphrase.sh and deploy/tokens.sh, then release.
#
# Prerequisites: `gcloud auth login` and `gh auth login`.
#
#   deploy/setup.sh [project-id]
set -euo pipefail
PROJECT=${1:-waiverwatch-509716}
REPO=moudlajs/bootleg
REGION=europe-west1
SERVICE=bootleg
AR_REPO=bootleg           # Artifact Registry repository
RUNTIME_SA=bootleg-run    # what the service runs as: no project roles, reads only its own secrets
DEPLOY_SA=bootleg-deploy  # what GitHub Actions deploys as
POOL=github               # shared with waiverwatch; each repo has its own provider
PROVIDER=github-bootleg
SECRETS=(bootleg-signing-key bootleg-passphrase bootleg-am-dev-token bootleg-am-user-token bootleg-am-storefront)

gc() { gcloud --project "$PROJECT" --quiet "$@"; }
say() { printf '\n== %s\n' "$*"; }
# New service accounts take a while to be usable in IAM policies.
retry() {
  local n
  for n in 1 2 3 4 5 6; do
    "$@" && return 0
    echo "  (not ready yet, retrying in 10s: attempt $n/6)" >&2
    sleep 10
  done
  "$@"
}

NUMBER=$(gcloud projects describe "$PROJECT" --format='value(projectNumber)')
RUNTIME_EMAIL="$RUNTIME_SA@$PROJECT.iam.gserviceaccount.com"
DEPLOY_EMAIL="$DEPLOY_SA@$PROJECT.iam.gserviceaccount.com"
POOL_ID="projects/$NUMBER/locations/global/workloadIdentityPools/$POOL"
# Cloud Run's deterministic URL, known before the first deploy.
BASE_URL="https://$SERVICE-$NUMBER.$REGION.run.app"

say "APIs"
gc services enable run.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
  iamcredentials.googleapis.com sts.googleapis.com secretmanager.googleapis.com

say "Artifact Registry: $AR_REPO in $REGION (keeps the 5 newest images)"
if ! gc artifacts repositories describe "$AR_REPO" --location "$REGION" >/dev/null 2>&1; then
  retry gc artifacts repositories create "$AR_REPO" --location "$REGION" --repository-format docker \
    --description "bootleg connector images"
fi
policy=$(mktemp)
trap 'rm -f "$policy"' EXIT
cat >"$policy" <<'JSON'
[
  {"name": "keep-newest-5", "action": {"type": "Keep"}, "mostRecentVersions": {"keepCount": 5}},
  {"name": "delete-older", "action": {"type": "Delete"}, "condition": {"tagState": "ANY"}}
]
JSON
gc artifacts repositories set-cleanup-policies "$AR_REPO" --location "$REGION" --policy "$policy" --no-dry-run >/dev/null

say "Service accounts"
for sa in "$RUNTIME_SA:bootleg runtime" "$DEPLOY_SA:GitHub Actions deploys bootleg"; do
  name=${sa%%:*}
  if ! gc iam service-accounts describe "$name@$PROJECT.iam.gserviceaccount.com" >/dev/null 2>&1; then
    gc iam service-accounts create "$name" --display-name "${sa#*:}"
  fi
done

say "Deployer permissions"
# run.admin (not run.developer) because making the service public needs setIamPolicy.
retry gc projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:$DEPLOY_EMAIL" \
  --role roles/run.admin --condition None >/dev/null
retry gc artifacts repositories add-iam-policy-binding "$AR_REPO" --location "$REGION" \
  --member "serviceAccount:$DEPLOY_EMAIL" --role roles/artifactregistry.writer >/dev/null
# Deploying a service that runs as RUNTIME_SA requires acting as it.
retry gc iam service-accounts add-iam-policy-binding "$RUNTIME_EMAIL" \
  --member "serviceAccount:$DEPLOY_EMAIL" --role roles/iam.serviceAccountUser >/dev/null

say "Secrets (readable only by $RUNTIME_SA)"
for s in "${SECRETS[@]}"; do
  if ! gc secrets describe "$s" >/dev/null 2>&1; then
    gc secrets create "$s" --replication-policy automatic
  fi
  retry gc secrets add-iam-policy-binding "$s" \
    --member "serviceAccount:$RUNTIME_EMAIL" --role roles/secretmanager.secretAccessor >/dev/null
done
# The token signing key: random, generated once straight into Secret
# Manager, never printed. Rotating it signs every client out.
if [ -z "$(gc secrets versions list bootleg-signing-key --filter state=enabled --format 'value(name)')" ]; then
  openssl rand -base64 48 | tr -d '\n' | gc secrets versions add bootleg-signing-key --data-file=- >/dev/null
  echo "  generated a signing key"
fi

say "Workload Identity Federation for $REPO (main branch and v* tags only)"
if ! gc iam workload-identity-pools describe "$POOL" --location global >/dev/null 2>&1; then
  gc iam workload-identity-pools create "$POOL" --location global --display-name "GitHub Actions"
fi
condition="assertion.repository == '$REPO' && (assertion.ref == 'refs/heads/main' || assertion.ref.startsWith('refs/tags/v'))"
if ! gc iam workload-identity-pools providers describe "$PROVIDER" --location global \
  --workload-identity-pool "$POOL" >/dev/null 2>&1; then
  gc iam workload-identity-pools providers create-oidc "$PROVIDER" --location global \
    --workload-identity-pool "$POOL" --display-name "GitHub OIDC (bootleg)" \
    --issuer-uri "https://token.actions.githubusercontent.com" \
    --attribute-mapping "google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref" \
    --attribute-condition "$condition"
fi
retry gc iam service-accounts add-iam-policy-binding "$DEPLOY_EMAIL" \
  --role roles/iam.workloadIdentityUser \
  --member "principalSet://iam.googleapis.com/$POOL_ID/attribute.repository/$REPO" >/dev/null

say "Repository variables on $REPO"
gh variable set GCP_PROJECT_ID -R "$REPO" -b "$PROJECT"
gh variable set GCP_REGION -R "$REPO" -b "$REGION"
gh variable set GCP_IMAGE -R "$REPO" -b "$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/$SERVICE"
gh variable set GCP_DEPLOY_SA -R "$REPO" -b "$DEPLOY_EMAIL"
gh variable set GCP_RUNTIME_SA -R "$REPO" -b "$RUNTIME_EMAIL"
gh variable set GCP_WIF_PROVIDER -R "$REPO" -b "$POOL_ID/providers/$PROVIDER"
gh variable set BOOTLEG_BASE_URL -R "$REPO" -b "$BASE_URL"

cat <<EOF

Done. Connector URL (after the first deploy): $BASE_URL/mcp
Next:
  deploy/passphrase.sh   # sign-in passphrase: copied to your clipboard, never printed
  deploy/tokens.sh       # Apple Music tokens from .env
  then tag a release; the release workflow deploys it.
EOF
