#!/usr/bin/env bash
#
# bootstrap.sh (QUM-870) — stand up the hub's REMOTE Terraform state backend.
#
# Solves the Terraform chicken-and-egg problem statelessly: creates the Azure
# resource group + storage account + blob container that hold TF state via
# imperative `az` CLI calls, so no local/in-repo Terraform state ever exists for
# this step. All other Terraform roots then use this container as their
# `backend "azurerm"` (see ../infra/terraform/backend-config.hcl.example).
#
# Idempotent: safe to re-run. Existing resources are not re-created; their tags
# and the account's blob-versioning are re-converged every run.
#
# Config is read from a GITIGNORED env file (default: ./bootstrap.env). NOTHING
# instance-specific is hardcoded here — this script is committed to a PUBLIC repo.
# Copy bootstrap.env.example -> bootstrap.env and fill in real values.
#
# Usage:
#   ./bootstrap.sh                       # uses ./bootstrap.env
#   BOOTSTRAP_ENV=/path/to.env ./bootstrap.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP_ENV="${BOOTSTRAP_ENV:-$SCRIPT_DIR/bootstrap.env}"
EXAMPLE_REL="bootstrap.env.example"

die() {
	echo "ERROR: $*" >&2
	exit 1
}

# --- load config (gitignored) ----------------------------------------------
[[ -f "$BOOTSTRAP_ENV" ]] || die "config file not found: $BOOTSTRAP_ENV
Copy $EXAMPLE_REL to bootstrap.env and fill in your values, then re-run."
# shellcheck disable=SC1090
source "$BOOTSTRAP_ENV"

# --- validate required config ----------------------------------------------
missing=()
[[ -n "${SUBSCRIPTION_ID:-}" ]] || missing+=(SUBSCRIPTION_ID)
[[ -n "${STATE_RG:-}" ]] || missing+=(STATE_RG)
[[ -n "${STATE_STORAGE_ACCOUNT:-}" ]] || missing+=(STATE_STORAGE_ACCOUNT)
[[ -n "${LOCATION:-}" ]] || missing+=(LOCATION)
if [[ "$(declare -p RESOURCE_TAGS 2>/dev/null)" != "declare -a"* ]] || ((${#RESOURCE_TAGS[@]} == 0)); then
	missing+=(RESOURCE_TAGS)
fi
((${#missing[@]} == 0)) || die "missing required config (${missing[*]}) in $BOOTSTRAP_ENV; see $EXAMPLE_REL"

STATE_CONTAINER="${STATE_CONTAINER:-tfstate}"
STATE_SKU="${STATE_SKU:-Standard_LRS}"

# --- pin the subscription EXPLICITLY on every operation --------------------
# HARD RULE (QUM-870): az defaults to the CLI's active subscription if none is
# given, which once landed resources in the wrong subscription. Pin it on every
# `az` call via SUB_ARGS, and export ARM_SUBSCRIPTION_ID so the Terraform
# azurerm backend/provider bind the same subscription without a separate flag.
export ARM_SUBSCRIPTION_ID="$SUBSCRIPTION_ID"
SUB_ARGS=(--subscription "$SUBSCRIPTION_ID")

# --- verify auth (never print identifiers) ---------------------------------
az account show "${SUB_ARGS[@]}" --output none 2>/dev/null || die "az cannot access subscription '$SUBSCRIPTION_ID' — either not authenticated (run 'az login') or the signed-in identity can't see this subscription / the id is wrong (see $EXAMPLE_REL)"

# --- resource group (dedicated, net-new; check-then-create) ----------------
# Per manager directive: this script CREATES its own dedicated state-backend RG
# and never reuses an existing corporate RG. On re-run we only converge tags.
if [[ "$(az group exists --name "$STATE_RG" "${SUB_ARGS[@]}")" == "true" ]]; then
	az group update --name "$STATE_RG" --tags "${RESOURCE_TAGS[@]}" "${SUB_ARGS[@]}" --output none
else
	az group create --name "$STATE_RG" --location "$LOCATION" --tags "${RESOURCE_TAGS[@]}" "${SUB_ARGS[@]}" --output none
fi

# --- storage account (globally-unique name → check-then-create) ------------
# Hardening: TLS1_2 min, HTTPS-only, public blob access OFF, infrastructure
# (double) encryption required, and shared-key (account-key/SAS) auth DISABLED
# so the account is AAD-only — the Terraform backend uses use_azuread_auth and
# never a key, matching corporate policy. At-rest SSE with Microsoft-managed
# keys is ON by default for StorageV2 — the flag below layers a second pass.
if az storage account show --name "$STATE_STORAGE_ACCOUNT" --resource-group "$STATE_RG" "${SUB_ARGS[@]}" --output none 2>/dev/null; then
	az storage account update \
		--name "$STATE_STORAGE_ACCOUNT" --resource-group "$STATE_RG" \
		--tags "${RESOURCE_TAGS[@]}" "${SUB_ARGS[@]}" --output none
else
	az storage account create \
		--name "$STATE_STORAGE_ACCOUNT" --resource-group "$STATE_RG" --location "$LOCATION" \
		--sku "$STATE_SKU" --kind StorageV2 \
		--min-tls-version TLS1_2 --https-only true \
		--allow-blob-public-access false \
		--require-infrastructure-encryption true \
		--allow-shared-key-access false \
		--tags "${RESOURCE_TAGS[@]}" "${SUB_ARGS[@]}" --output none
fi

# --- blob versioning (desired-state; idempotent, ensured every run) --------
az storage account blob-service-properties update \
	--account-name "$STATE_STORAGE_ACCOUNT" --resource-group "$STATE_RG" \
	--enable-versioning true "${SUB_ARGS[@]}" --output none

# --- grant deploying identity data-plane access (AAD end-to-end, no keys) ---
# Shared-key auth is off the table (corporate policy). Assign the "Storage Blob
# Data Contributor" role scoped to THIS storage account to the signed-in
# identity, so `--auth-mode login` (below) and `use_azuread_auth = true` in the
# Terraform backend both work without any account key.
PRINCIPAL_ID="$(az ad signed-in-user show --query id -o tsv 2>/dev/null || true)"
[[ -n "$PRINCIPAL_ID" ]] || die "could not resolve the signed-in identity's object id (az ad signed-in-user show)"
SA_ID="$(az storage account show --name "$STATE_STORAGE_ACCOUNT" --resource-group "$STATE_RG" "${SUB_ARGS[@]}" --query id -o tsv)"
if ! az role assignment list --assignee "$PRINCIPAL_ID" --role "Storage Blob Data Contributor" --scope "$SA_ID" "${SUB_ARGS[@]}" --query "[0].id" -o tsv 2>/dev/null | grep -q .; then
	az role assignment create --assignee "$PRINCIPAL_ID" --role "Storage Blob Data Contributor" --scope "$SA_ID" "${SUB_ARGS[@]}" --output none ||
		die "role assignment DENIED — the deploying identity lacks Owner/User Access Administrator on the scope. STOP: shared-key auth is off the table. Ask the operator to grant 'Storage Blob Data Contributor' on this storage account out-of-band, then re-run."
fi

# --- state container (private; idempotent) ---------------------------------
# `--auth-mode login` uses your AAD identity and needs a DATA-PLANE role
# ("Storage Blob Data Contributor") on the account — control-plane Owner/
# Contributor is not enough. Right after account creation that role assignment
# may not have propagated yet, so retry a few times before failing.
for attempt in 1 2 3 4 5; do
	if az storage container create \
		--name "$STATE_CONTAINER" --account-name "$STATE_STORAGE_ACCOUNT" \
		--auth-mode login --public-access off "${SUB_ARGS[@]}" --output none 2>/dev/null; then
		break
	fi
	if ((attempt == 5)); then
		die "container create failed after 5 attempts — ensure your identity has the 'Storage Blob Data Contributor' role on $STATE_STORAGE_ACCOUNT (RBAC propagation can take ~1 min after account creation)"
	fi
	sleep 15
done

echo "Bootstrap complete. Remote Terraform state backend is ready."
echo "Next: copy ../infra/terraform/backend-config.hcl.example to backend-config.hcl,"
echo "fill in the values from your bootstrap.env, and run 'terraform init -backend-config=backend-config.hcl'."
