#!/usr/bin/env bash
# One-time, re-runnable GCP setup for bootleg: keyless GitHub deploys, secrets, budget and billing kill switch.
# Needs billing linked, gcloud/gh logged in and Docker; afterwards run deploy/passphrase.sh, deploy/tokens.sh, then release.
#   deploy/setup.sh [project-id] [billing-account-id]
set -euo pipefail
PROJECT=${1:-bootleg-638112}
BILLING=${2:-01D49B-E5CDCF-22ECEC}
BUDGET_CZK=25             # about $1: any spend at all is a problem
REPO=moudlajs/bootleg
REGION=europe-west1
SERVICE=bootleg
AR_REPO=bootleg
RUNTIME_SA=bootleg-run    # no project roles, reads only its own secrets
DEPLOY_SA=bootleg-deploy
POOL=github
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
  iamcredentials.googleapis.com sts.googleapis.com secretmanager.googleapis.com \
  pubsub.googleapis.com cloudbilling.googleapis.com cloudresourcemanager.googleapis.com \
  billingbudgets.googleapis.com

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
# Signing key goes straight into Secret Manager, never printed; rotating it signs every client out.
if [ -z "$(gc secrets versions list bootleg-signing-key --filter state=enabled --format 'value(name)')" ]; then
  openssl rand -base64 48 | tr -d '\n' | gc secrets versions add bootleg-signing-key --data-file=- >/dev/null
  echo "  generated a signing key"
fi

say "Workload Identity Federation for $REPO (main branch and v* tags only)"
if ! gc iam workload-identity-pools describe "$POOL" --location global >/dev/null 2>&1; then
  gc iam workload-identity-pools create "$POOL" --location global --display-name "GitHub Actions"
fi
# Only this repo's main branch and v* tags may deploy.
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

say "Budget alert: $BUDGET_CZK CZK/month on $PROJECT"
budgets=$(gcloud billing budgets list --billing-account "$BILLING" --format='value(displayName)')
if ! grep -qx bootleg <<<"$budgets"; then
  gcloud billing budgets create --billing-account "$BILLING" --display-name bootleg \
    --budget-amount "${BUDGET_CZK}CZK" --filter-projects "projects/$PROJECT" \
    --threshold-rule percent=0.5 --threshold-rule percent=1.0
fi

say "Billing kill switch"
# Budget -> Pub/Sub -> bootleg-killswitch (waiverwatch's image), which unlinks billing when cost reaches the budget.
TOPIC=billing-budget
KILL_SA=killswitch
PUSH_SA=killswitch-push
KILL_EMAIL="$KILL_SA@$PROJECT.iam.gserviceaccount.com"
PUSH_EMAIL="$PUSH_SA@$PROJECT.iam.gserviceaccount.com"
for sa in "$KILL_SA:billing kill switch" "$PUSH_SA:Pub/Sub push to the kill switch"; do
  name=${sa%%:*}
  if ! gc iam service-accounts describe "$name@$PROJECT.iam.gserviceaccount.com" >/dev/null 2>&1; then
    gc iam service-accounts create "$name" --display-name "${sa#*:}"
  fi
done
# projectManager to unlink billing; browser to read billing info first.
for role in roles/billing.projectManager roles/browser; do
  retry gc projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:$KILL_EMAIL" \
    --role "$role" --condition None >/dev/null
done
if ! gc pubsub topics describe "$TOPIC" >/dev/null 2>&1; then
  gc pubsub topics create "$TOPIC"
fi
# Cloud Billing publishes budget notifications as this Google-managed account.
retry gc pubsub topics add-iam-policy-binding "$TOPIC" \
  --member serviceAccount:billing-budget-alert@system.gserviceaccount.com \
  --role roles/pubsub.publisher >/dev/null
WW_TAG=$(gh release view --repo moudlajs/waiverwatch --json tagName --jq .tagName)
KILL_IMAGE="$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/killswitch:$WW_TAG"
if ! gc artifacts docker images describe "$KILL_IMAGE" >/dev/null 2>&1; then
  src="$REGION-docker.pkg.dev/waiverwatch-509716/waiverwatch/waiverwatch:$WW_TAG"
  gcloud auth configure-docker "$REGION-docker.pkg.dev" --quiet >/dev/null 2>&1
  docker pull --platform linux/amd64 -q "$src" >/dev/null
  # Fail now, not at the deploy below, if the release lost /killswitch.
  cid=$(docker create --platform linux/amd64 "$src")
  if ! docker cp "$cid:/killswitch" - >/dev/null 2>&1; then
    docker rm "$cid" >/dev/null
    echo "waiverwatch $WW_TAG has no /killswitch; pick another tag" >&2
    exit 1
  fi
  docker rm "$cid" >/dev/null
  docker tag "$src" "$KILL_IMAGE"
  docker push -q "$KILL_IMAGE" >/dev/null
  echo "  copied kill switch image from waiverwatch $WW_TAG"
fi
# Log the digest: tags can move.
echo "  kill switch image digest: $(gc artifacts docker images describe "$KILL_IMAGE" --format 'value(image_summary.digest)')"
gc run deploy bootleg-killswitch --region "$REGION" --image "$KILL_IMAGE" \
  --command /killswitch --service-account "$KILL_EMAIL" --no-allow-unauthenticated \
  --min-instances 0 --max-instances 1 --cpu 1 --memory 256Mi --timeout 60 \
  --set-env-vars "KILLSWITCH_PROJECT=$PROJECT,KILLSWITCH_DRY_RUN=${KILLSWITCH_DRY_RUN:-0}" >/dev/null
KILL_URL=$(gc run services describe bootleg-killswitch --region "$REGION" --format 'value(status.url)')
retry gc run services add-iam-policy-binding bootleg-killswitch --region "$REGION" \
  --member "serviceAccount:$PUSH_EMAIL" --role roles/run.invoker >/dev/null
if ! gc pubsub subscriptions describe killswitch >/dev/null 2>&1; then
  retry gc pubsub subscriptions create killswitch --topic "$TOPIC" \
    --push-endpoint "$KILL_URL" --push-auth-service-account "$PUSH_EMAIL" \
    --ack-deadline 60 --message-retention-duration 1d
fi
budget=$(gcloud billing budgets list --billing-account "$BILLING" \
  --filter 'displayName=bootleg' --format 'value(name)')
gcloud billing budgets update "$budget" --billing-account "$BILLING" \
  --notifications-rule-pubsub-topic "projects/$PROJECT/topics/$TOPIC" >/dev/null
echo "  kill switch at $KILL_URL (dry run: ${KILLSWITCH_DRY_RUN:-0})"

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
