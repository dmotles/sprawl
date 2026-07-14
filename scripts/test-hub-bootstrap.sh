#!/usr/bin/env bash
#
# test-hub-bootstrap.sh (QUM-870)
#
# Exercises deploy/hub/bootstrap/bootstrap.sh — the stateless, idempotent az
# CLI bootstrap for the hub's remote Terraform state backend — WITHOUT touching
# any real cloud. A fake `az` shim on PATH records the commands the script would
# run and returns canned responses; the test asserts against that log.
#
# Covers:
#   1. Config refusal    — missing required config → non-zero exit, points at
#                          bootstrap.env.example, and NO az create calls.
#   2. Fresh create path — RG + storage account + versioning + container created
#                          with the required hardening flags.
#   3. Idempotent re-run — pre-existing RG + storage account → no re-create.
#   4. No-leak guard     — no corporate Azure specifics in tracked deploy/hub.
#   5. Ignore guard      — real config/state/backend-config are gitignored;
#                          committed .example templates are NOT.
#
# Needs only bash + git (no cloud, no sprawl binary, no claude).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BOOTSTRAP="$REPO_ROOT/deploy/hub/bootstrap/bootstrap.sh"

PASS=0
FAIL=0
pass() {
	PASS=$((PASS + 1))
	echo "  PASS: $1"
}
fail() {
	FAIL=$((FAIL + 1))
	echo "  FAIL: $1" >&2
}
assert() {
	if eval "$2"; then pass "$1"; else fail "$1 (cmd: $2)"; fi
}

WORKDIR="$(mktemp -d "/tmp/sprawl-hub-bootstrap.XXXXXX")"
cleanup() {
	# Destructive-var guardrail: only ever rm under /tmp.
	[[ "$WORKDIR" == /tmp/* ]] || exit 1
	rm -rf "$WORKDIR"
}
trap cleanup EXIT

# --- fake az shim ----------------------------------------------------------
# Logs every invocation to $AZ_LOG and returns canned output. Existence of the
# RG / storage account is toggled per-run via FAKE_RG_EXISTS / FAKE_SA_EXISTS.
SHIM_DIR="$WORKDIR/bin"
mkdir -p "$SHIM_DIR"
cat >"$SHIM_DIR/az" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
# Record the exported ARM_SUBSCRIPTION_ID the shim inherits from bootstrap.sh, so
# the test can prove the export (child processes only see it if it was exported).
echo "env ARM_SUBSCRIPTION_ID=${ARM_SUBSCRIPTION_ID:-UNSET}" >>"$AZ_LOG"
echo "az $*" >>"$AZ_LOG"
sub="${1:-}"; obj="${2:-}"; verb="${3:-}"
case "$sub $obj $verb" in
"account show "*) exit 0 ;;
"ad signed-in-user show"*) echo "00000000-0000-0000-0000-fakeobjectid0"; exit 0 ;;
"group exists "*) [[ "${FAKE_RG_EXISTS:-false}" == "true" ]] && echo true || echo false ;;
"group create "*) exit 0 ;;
"storage account show"*)
	# A `--query id` lookup happens after the account is ensured to exist, so
	# it always resolves; the bare existence probe honors FAKE_SA_EXISTS.
	if [[ "$*" == *"--query id"* ]]; then echo "/fake/scope/storageAccounts/sa"; exit 0; fi
	[[ "${FAKE_SA_EXISTS:-false}" == "true" ]] && exit 0 || { echo "not found" >&2; exit 1; } ;;
"storage account create"*) exit 0 ;;
"storage account update"*) exit 0 ;;
"storage account blob-service-properties") exit 0 ;;
"storage container create"*) exit 0 ;;
"role assignment list"*) exit 0 ;;  # no output → not yet assigned → create fires
"role assignment create"*) exit 0 ;;
*) exit 0 ;;
esac
SHIM
chmod +x "$SHIM_DIR/az"

# run_bootstrap <config-file> — invoke bootstrap.sh with the fake az shim on
# PATH and a given BOOTSTRAP_ENV. Sets globals RUN_RC / RUN_OUT / AZ_LOG.
RUN_RC=0
RUN_OUT=""
run_bootstrap() {
	local cfg="$1"
	AZ_LOG="$WORKDIR/az.log.$RANDOM"
	: >"$AZ_LOG"
	export AZ_LOG
	RUN_RC=0
	# Neutralize any ARM_SUBSCRIPTION_ID inherited from the developer's shell, so
	# the only path to the pin is bootstrap.sh's own export (avoids false pass/fail).
	RUN_OUT="$(env -u ARM_SUBSCRIPTION_ID PATH="$SHIM_DIR:$PATH" BOOTSTRAP_ENV="$cfg" \
		FAKE_RG_EXISTS="${FAKE_RG_EXISTS:-false}" FAKE_SA_EXISTS="${FAKE_SA_EXISTS:-false}" \
		bash "$BOOTSTRAP" 2>&1)" || RUN_RC=$?
}

if [[ ! -f "$BOOTSTRAP" ]]; then
	echo "FATAL: bootstrap script not found at $BOOTSTRAP" >&2
	exit 1
fi

# ---------------------------------------------------------------------------
echo "== Case 1: missing config is refused (no cloud calls) =="
MISSING="$WORKDIR/absent.env"
run_bootstrap "$MISSING"
assert "missing config exits non-zero" "[[ $RUN_RC -ne 0 ]]"
assert "error points operator at bootstrap.env.example" "echo \"\$RUN_OUT\" | grep -q 'bootstrap.env.example'"
assert "no storage account create attempted on refusal" "! grep -q 'storage account create' \"\$AZ_LOG\""
assert "no group create attempted on refusal" "! grep -q 'group create' \"\$AZ_LOG\""
assert "no az call at all on refusal" "! grep -q '^az ' \"\$AZ_LOG\""

# ---------------------------------------------------------------------------
echo "== Case 1b: missing SUBSCRIPTION_ID is refused (no cloud calls) =="
# Subscription must be pinned explicitly — an unset SUBSCRIPTION_ID would let az
# fall back to the CLI default subscription (the QUM-870 wrong-subscription bug).
NOSUB="$WORKDIR/nosub.env"
cat >"$NOSUB" <<'ENV'
STATE_RG="rg-fake-tfstate"
STATE_STORAGE_ACCOUNT="stfaketfstate001"
STATE_CONTAINER="tfstate"
LOCATION="fakeregion"
RESOURCE_TAGS=(owner=faketester long_running=true department=fake "purpose=fake purpose")
ENV
run_bootstrap "$NOSUB"
assert "missing SUBSCRIPTION_ID exits non-zero" "[[ $RUN_RC -ne 0 ]]"
assert "error names SUBSCRIPTION_ID" "echo \"\$RUN_OUT\" | grep -q 'SUBSCRIPTION_ID'"
assert "no group create attempted without subscription" "! grep -q 'group create' \"\$AZ_LOG\""
assert "no storage account create attempted without subscription" "! grep -q 'storage account create' \"\$AZ_LOG\""
assert "no az call at all without subscription" "! grep -q '^az ' \"\$AZ_LOG\""

# az_line <pattern> — echo the (first) az invocation line in $AZ_LOG matching
# <pattern>, so flag assertions are bound to the SPECIFIC command, not the whole
# log (a flag on the wrong command must not satisfy the assertion).
az_line() { grep -m1 -- "$1" "$AZ_LOG" 2>/dev/null || true; }

# ---------------------------------------------------------------------------
echo "== Case 2: fresh create path emits hardened resources =="
GOOD="$WORKDIR/good.env"
cat >"$GOOD" <<'ENV'
STATE_RG="rg-fake-tfstate"
STATE_STORAGE_ACCOUNT="stfaketfstate001"
STATE_CONTAINER="tfstate"
LOCATION="fakeregion"
# Subscription pin — a throwaway non-GUID fake (real values are gitignored-only).
SUBSCRIPTION_ID="fakesub-1234"
# Tag VALUES are gitignored-config-only; these are throwaway fakes for the test.
RESOURCE_TAGS=(owner=faketester long_running=true department=fake "purpose=fake purpose")
ENV
FAKE_RG_EXISTS=false FAKE_SA_EXISTS=false run_bootstrap "$GOOD"
assert "fresh run exits 0" "[[ $RUN_RC -eq 0 ]]"
assert "resource group created" "grep -q 'group create' \"\$AZ_LOG\""
assert "storage account created" "grep -q 'storage account create' \"\$AZ_LOG\""
# Hardening flags must ride on the `storage account create` line specifically.
assert "public blob access disabled on account create" "az_line 'storage account create' | grep -q -- '--allow-blob-public-access false'"
assert "TLS1_2 minimum enforced on account create" "az_line 'storage account create' | grep -q -- '--min-tls-version TLS1_2'"
assert "infrastructure encryption required on account create" "az_line 'storage account create' | grep -q -- '--require-infrastructure-encryption true'"
assert "shared-key auth disabled on account create (AAD-only)" "az_line 'storage account create' | grep -q -- '--allow-shared-key-access false'"
# Mandatory tags must ride on the RG + account create lines (sourced from config).
assert "RG create carries tags" "az_line 'group create' | grep -q -- '--tags'"
assert "RG create carries owner tag from config" "az_line 'group create' | grep -q 'owner=faketester'"
assert "account create carries tags" "az_line 'storage account create' | grep -q -- '--tags'"
assert "account create carries owner tag from config" "az_line 'storage account create' | grep -q 'owner=faketester'"
# Versioning is a blob-service-property, not an account-create flag.
assert "blob versioning enabled" "az_line 'blob-service-properties' | grep -q -- '--enable-versioning true'"
assert "tfstate container created" "az_line 'storage container create' | grep -q -- '--name tfstate'"
# AAD end-to-end: grant the deploying identity a data-plane role scoped to the
# SA, then create the container with --auth-mode login (NO shared-key auth).
assert "data-plane role assigned (Storage Blob Data Contributor)" "az_line 'role assignment create' | grep -q 'Storage Blob Data Contributor'"
assert "role assignment scoped to the storage account" "az_line 'role assignment create' | grep -q -- '--scope'"
assert "container created with AAD auth (--auth-mode login)" "az_line 'storage container create' | grep -q -- '--auth-mode login'"
# Role grant must precede the container create it enables.
ra=$(grep -n 'role assignment create' "$AZ_LOG" | head -1 | cut -d: -f1) || ra=""
cc=$(grep -n 'storage container create' "$AZ_LOG" | head -1 | cut -d: -f1) || cc=""
assert "role assignment precedes container create" "[[ -n '$ra' && -n '$cc' && '$ra' -lt '$cc' ]]"
# Subscription pin: EVERY resource-scoped az call must carry --subscription so the
# CLI default subscription can never be targeted by accident (the QUM-870 bug).
assert "account show pins --subscription" "az_line 'az account show' | grep -q -- '--subscription'"
assert "group create pins --subscription" "az_line 'group create' | grep -q -- '--subscription'"
assert "storage account create pins --subscription" "az_line 'storage account create' | grep -q -- '--subscription'"
assert "blob-service-properties pins --subscription" "az_line 'blob-service-properties' | grep -q -- '--subscription'"
assert "role assignment create pins --subscription" "az_line 'role assignment create' | grep -q -- '--subscription'"
assert "storage container create pins --subscription" "az_line 'storage container create' | grep -q -- '--subscription'"
assert "subscription value sourced from config" "az_line 'storage account create' | grep -q -- '--subscription fakesub-1234'"
# The backend / future terraform reads ARM_SUBSCRIPTION_ID — bootstrap must export it.
assert "ARM_SUBSCRIPTION_ID exported to child az calls" "grep -q 'env ARM_SUBSCRIPTION_ID=fakesub-1234' \"\$AZ_LOG\""
assert "no az call ran before ARM_SUBSCRIPTION_ID export" "! grep -q 'env ARM_SUBSCRIPTION_ID=UNSET' \"\$AZ_LOG\""

# ---------------------------------------------------------------------------
echo "== Case 3: idempotent re-run does not re-create; converges tags =="
FAKE_RG_EXISTS=true FAKE_SA_EXISTS=true run_bootstrap "$GOOD"
assert "idempotent run exits 0" "[[ $RUN_RC -eq 0 ]]"
assert "existing RG not re-created" "! grep -q 'group create' \"\$AZ_LOG\""
assert "existing storage account not re-created" "! grep -q 'storage account create' \"\$AZ_LOG\""
# Desired-state hardening + tags must still converge on existing resources.
assert "versioning re-asserted on existing account" "az_line 'blob-service-properties' | grep -q -- '--enable-versioning true'"
assert "RG tags converged via update" "az_line 'group update' | grep -q -- '--tags'"
assert "account tags converged via update" "az_line 'storage account update' | grep -q -- '--tags'"
# Subscription pin must ride the convergence (update) commands too.
assert "group update pins --subscription" "az_line 'group update' | grep -q -- '--subscription'"
assert "storage account update pins --subscription" "az_line 'storage account update' | grep -q -- '--subscription'"

# ---------------------------------------------------------------------------
echo "== Case 4: no corporate Azure specifics in tracked deploy/hub =="
# Binds against whatever is tracked under deploy/hub (the .example templates,
# scripts, and docs are committed, so this is real coverage — not vacuous).
mapfile -t TRACKED < <(git -C "$REPO_ROOT" ls-files 'deploy/hub')
# leak_hits <pattern> — 0 (match found) iff <pattern> appears in any tracked
# deploy/hub file. Avoids a short-circuiting `| grep -q` pipe: under pipefail a
# SIGPIPE on the producing loop would otherwise mask a real match (false clean).
leak_hits() {
	local pat="$1" f hits=""
	((${#TRACKED[@]})) || return 1
	for f in "${TRACKED[@]}"; do
		[[ -f "$REPO_ROOT/$f" ]] || continue
		hits+="$(grep -InEi -- "$pat" "$REPO_ROOT/$f" || true)"
	done
	[[ -n "$hits" ]]
}
# leak_hits_fixed <literal> — like leak_hits but matches a FIXED string (grep -F),
# so real deployment values with regex metacharacters are compared literally.
leak_hits_fixed() {
	local lit="$1" f hits=""
	((${#TRACKED[@]})) || return 1
	for f in "${TRACKED[@]}"; do
		[[ -f "$REPO_ROOT/$f" ]] || continue
		hits+="$(grep -FIni -- "$lit" "$REPO_ROOT/$f" || true)"
	done
	[[ -n "$hits" ]]
}
assert "no GUID (subscription/tenant) in tracked deploy/hub" "! leak_hits '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}'"
assert "no westus region hardcoded in tracked deploy/hub" "! leak_hits 'westus'"
# The employer/company name can't be grepped for without writing it in this
# PUBLIC repo, so instead assert the REAL deployment values actually in use —
# sourced from the gitignored bootstrap.env / backend-config.hcl — never appear
# in a tracked deploy/hub file. Stronger and self-updating vs. a hardcoded name.
# Skips (with a note) in a clean checkout where the gitignored config is absent.
REAL_ENV="$REPO_ROOT/deploy/hub/bootstrap/bootstrap.env"
REAL_BACKEND="$REPO_ROOT/deploy/hub/infra/terraform/backend-config.hcl"
if [[ -f "$REAL_ENV" ]]; then
	# shellcheck disable=SC1090
	source "$REAL_ENV"
	real_leak=""
	real_vals=("${SUBSCRIPTION_ID:-}" "${STATE_RG:-}" "${STATE_STORAGE_ACCOUNT:-}" "${LOCATION:-}")
	((${#RESOURCE_TAGS[@]})) && real_vals+=("${RESOURCE_TAGS[@]}")
	for v in "${real_vals[@]}"; do
		[[ -n "$v" ]] || continue
		leak_hits_fixed "$v" && real_leak+="[$v] "
	done
	assert "no real deployment values leak into tracked deploy/hub" "[[ -z '$real_leak' ]]"
else
	echo "  SKIP: no gitignored bootstrap.env — real-value leak scan skipped (GUID/region checks still run)"
fi

# ---------------------------------------------------------------------------
echo "== Case 5: real config gitignored, .example templates tracked =="
gi() { git -C "$REPO_ROOT" check-ignore -q "$1"; }
assert "bootstrap.env is gitignored" "gi deploy/hub/bootstrap/bootstrap.env"
assert "backend-config.hcl is gitignored" "gi deploy/hub/infra/terraform/backend-config.hcl"
assert "bootstrap.env.example is NOT gitignored" "! gi deploy/hub/bootstrap/bootstrap.env.example"
assert "backend-config.hcl.example is NOT gitignored" "! gi deploy/hub/infra/terraform/backend-config.hcl.example"

# ---------------------------------------------------------------------------
echo "== Case 6: subscription-pinning MECHANISM is committed (placeholders only) =="
# The pin is committed as the pattern (env var + backend key + provider template);
# only the VALUE stays gitignored. Placeholders must NOT be real GUIDs (Case 4).
ENV_EXAMPLE="$REPO_ROOT/deploy/hub/bootstrap/bootstrap.env.example"
BACKEND_EXAMPLE="$REPO_ROOT/deploy/hub/infra/terraform/backend-config.hcl.example"
PROVIDERS_EXAMPLE="$REPO_ROOT/deploy/hub/infra/terraform/providers.tf.example"
assert "bootstrap.env.example declares SUBSCRIPTION_ID" "grep -q 'SUBSCRIPTION_ID' \"\$ENV_EXAMPLE\""
assert "backend-config.hcl.example declares subscription_id" "grep -q 'subscription_id' \"\$BACKEND_EXAMPLE\""
assert "providers.tf.example exists as the azurerm provider pattern" "[[ -f \"\$PROVIDERS_EXAMPLE\" ]]"
assert "providers.tf.example pins subscription_id" "grep -q 'subscription_id' \"\$PROVIDERS_EXAMPLE\""
assert "providers.tf.example is tracked (NOT gitignored)" "! gi deploy/hub/infra/terraform/providers.tf.example"
# Placeholder intent is local to Case 6 (belt-and-suspenders with Case 4's GUID grep).
assert "backend-config.hcl.example subscription placeholder is a <...> token" "grep -Eq 'subscription_id[[:space:]]*=[[:space:]]*\"<[^>]+>\"' \"\$BACKEND_EXAMPLE\""

# ---------------------------------------------------------------------------
echo
echo "RESULTS: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]]
