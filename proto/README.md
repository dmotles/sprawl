# Hub proto contract

The `.proto` files here are the single source of truth for the sprawl hub's
Connect/protobuf wire contract (host↔hub and api↔webapp). See
[`docs/design/hub/03-api-surfaces.md`](../docs/design/hub/03-api-surfaces.md).

## Additive-only field policy (load-bearing)

Three deployables — the host (`sprawl enter`), the hub (`hubd`), and the browser
SPA — update independently and on different cadences. Wire **skew is the normal
state, not an error**: an old host may talk to a new hub for weeks; a stale
browser tab may outlive several hub deploys. The contract must tolerate this.

Rules:

1. **Never change a field's number.**
2. **Never change a field's type.**
3. **Only add new fields, each with a new, previously-unused number.**
4. **Never reuse a retired field number or name** — `reserved` it instead:
   ```proto
   reserved 3, 5 to 7;
   reserved "old_field_name";
   ```
5. New capabilities are new optional fields or new RPCs; old clients ignore what
   they don't know.

## Enforcement

`make validate` runs (via the `proto-check` target):

- `buf lint` — schema consistency (STANDARD ruleset).
- `buf format --diff --exit-code` — formatting check (no writes).
- `buf breaking --against '.git#branch=main'` — rejects any wire-incompatible
  change against the `main` HEAD baseline.

### Baseline choice (Open Question H1)

`buf breaking` is tracked against **`main` HEAD** (`.git#branch=main`) for now,
rather than the last-released hub tag. Rationale: there is no released hub tag
yet, and `main` HEAD is the shape every in-flight deployable is built from. If
in-flight `main` churn ever starts blocking PRs, revisit and pin to a released
tag (see docs 03 / 08 Open Questions).

**First-landing bootstrap.** The `proto-check` make target skips `buf breaking`
until `main` carries a root `buf.yaml` (i.e. until this slice merges). Reason:
without a root `buf.yaml` on the baseline, buf's default whole-tree scan of
`main` reaches into the throwaway transport spike (`deploy/hub/spike/`) and
mis-reports its proto as "deleted". There is genuinely no product-proto baseline
to break against before this lands, so the skip is correct — and it self-heals
the moment `buf.yaml` exists on `main`, after which every subsequent change is
gated for real.

## Code generation

- Go (connect-go) → `internal/hub/gen/` via the default `buf.gen.yaml`
  (`make proto-gen`). Generated code is committed.
- TypeScript (connect-es) → `web/gen/` via the opt-in `buf.gen.web.yaml`
  (`make proto-gen-web`), which needs the npm `protoc-gen-es` /
  `protoc-gen-connect-es` tools on PATH. Until the SPA lands, a committed stub
  (`web/gen/hub/v1/hub_pb.stub.ts`) holds the seam so `make validate` never
  depends on a node toolchain.
