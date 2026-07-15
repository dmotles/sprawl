# Hub transport spike (QUM-871)

**Throwaway** spike proving the hub's downlink assumption on a real managed
container platform: a heartbeated Connect **server-stream** survives a managed
L7 (Envoy) ingress idle timeout (~240s on Azure Container Apps), and reconnect
with `from_seq` resumes with **zero gaps / zero dupes** (the "one rule"). See
[`docs/design/hub/03-api-surfaces.md` §4–5](../../../docs/design/hub/03-api-surfaces.md)
and [`13-implementation-plan.md` §2](../../../docs/design/hub/13-implementation-plan.md).

Deliberately minimal app logic. The infra path (remote state + ACA) is reusable
for Phase 0; the spike **app** resources are torn down after the verdict.

## Layout

```
proto/                 # Connect service definition (server-streaming Subscribe)
gen/                   # generated protobuf + connect code (checked in)
internal/stream/       # pure from_seq resume logic (offline-unit-tested)
cmd/server/            # h2c Connect server: seq'd HEARTBEAT + DATA frames
cmd/client/            # evidence client: logs frames / gaps / disconnects
infra/                 # parameterized Terraform root (azurerm remote backend → ACA)
Dockerfile             # static server image
logs/                  # client evidence output (gitignored)
```

## Hygiene (PUBLIC repo)

Nothing Azure-account-specific is committed. Real values (subscription, RG /
resource names, region, tag values) live **only** in gitignored files:
`infra/spike.tfvars` and `infra/backend-config.hcl`. Committed files are the
`.example` templates (placeholders only). Every `az`/`terraform` invocation pins
the subscription (`ARM_SUBSCRIPTION_ID` / provider `subscription_id` /
`--subscription`).

## Build & regenerate

```bash
# regenerate proto (only if proto changes; requires buf + protoc-gen-{go,connect-go} on PATH)
buf generate

go test ./internal/stream/   # offline reconnect-rule test
go build ./...               # server + client
```

## Local smoke (no cloud)

```bash
HEARTBEAT_INTERVAL_SECONDS=2 PORT=8080 go run ./cmd/server &
go run ./cmd/client -addr http://localhost:8080 -insecure -duration 10s
# reconnect: resumes at from_seq+1, no gaps/dupes
go run ./cmd/client -addr http://localhost:8080 -insecure -from-seq 5 -duration 5s
```

## Deploy (behind the corporate-cloud gate)

```bash
cd infra
cp spike.tfvars.example spike.tfvars               # fill in real values
cp backend-config.hcl.example backend-config.hcl   # QUM-870 state backend, key=spike.tfstate
export ARM_SUBSCRIPTION_ID=<subscription-id>
terraform init -backend-config=backend-config.hcl
terraform plan -var-file=spike.tfvars              # placeholder public image; plan is read-only
# --- STOP: report plan + inventory, wait for approval, THEN apply ---
```

`terraform plan` uses a public placeholder image so it succeeds before the ACR
image exists. After approval: `az acr build` the server image to the throwaway
ACR, then `terraform apply -var-file=spike.tfvars -var container_image=<acr>/hubspike:<tag>`.

## Tests (on the deployed app)

- **Test 1 — idle survival:** hold the stream open ≥2× the ingress idle timeout
  (~8–10 min) with heartbeats only; confirm no L7 drop. Record which mechanism
  (HTTP/2 PING vs on-stream DATA) and interval worked.
- **Test 2 — reconnect delta:** kill mid-stream, reconnect with `-reconnect`
  (resumes from the last seen seq); confirm zero gaps/dupes.

Client stdout → `logs/` is the evidence artifact.

## Teardown

```bash
cd infra && terraform destroy -var-file=spike.tfvars
```

Destroys only the spike app RG and its contents; the QUM-870 state backend is
untouched.
