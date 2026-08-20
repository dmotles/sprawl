# Enabling the event log (QUM-1249, M1a)

**Date:** 2026-08-20
**Status:** current for M1a. The event log is **off by default** and nothing in
sprawl requires it — if you never enable it, every command behaves exactly as it
did before.

This is the contributor-facing setup guide. For *why* the event log exists and
what it becomes, read
[`docs/designs/v2-log-centric-rearchitecture.md`](designs/v2-log-centric-rearchitecture.md);
this file is only about getting one running.

## What you need

Any **Postgres ≥ 16**. That is the whole requirement.

pgvector is **not** needed for M1a. The design names it because M4 adds
`event_embeddings` with an HNSW index, but M1a creates no vector columns and its
migrations deliberately do not `CREATE EXTENSION vector` — so a plain Postgres 16
is enough, and a pin in the test suite keeps it that way until M4 removes it.

Three options, cheapest first.

### Option 1 — a local container (best for development)

```bash
docker run -d --name sprawl-eventlog \
  -e POSTGRES_USER=sprawl -e POSTGRES_PASSWORD=sprawl -e POSTGRES_DB=sprawl \
  -p 5432:5432 postgres:16-alpine
```

DSN: `postgres://sprawl:sprawl@127.0.0.1:5432/sprawl?sslmode=disable`

This is throwaway and unencrypted, which is fine for a laptop and not fine for
anything shared. Note the integration suite does **not** use this container — it
starts its own on a random port via testcontainers, so the two cannot collide.

### Option 2 — Neon's free tier (best if you have no local Docker)

[Neon](https://neon.com) offers a free serverless Postgres tier that is enough
for a single contributor's event log. Create a project, pick Postgres 16 or
later, and copy the connection string it gives you — it is already in
`postgres://…` form and already includes `sslmode=require`.

Two things to know before relying on it:

- **Free-tier projects suspend when idle** and take a moment to wake. Sprawl
  handles this correctly rather than hanging — an unreachable database puts the
  store in degraded mode, telemetry spills to disk, and agents keep running
  (see [Degraded mode](#degraded-mode)) — but you will see gaps in the log
  across a suspend, and a suspend during startup means that process runs
  degraded until it is restarted. Degraded mode does not reconnect in place.
- Free tiers change. Treat the specifics above as a starting point and the
  provider's own docs as authoritative.

### Option 3 — a managed instance you own

Any Postgres ≥ 16 works; nothing in the code is vendor-bound. The design doc
names Azure Database for PostgreSQL Flexible Server B1ms as a generic
public-cloud reference point for cost, not as a requirement.

## Turning it on

Two steps, and they are separate on purpose.

### 1. Give sprawl the DSN — never in a tracked file

```bash
export SPRAWL_DB_DSN='postgres://user:password@host:5432/sprawl?sslmode=require'
```

or, to persist it, `~/.config/sprawl/secrets.yaml`:

```yaml
db_dsn: postgres://user:password@host:5432/sprawl?sslmode=require
```

```bash
chmod 600 ~/.config/sprawl/secrets.yaml
```

**The 0600 is enforced, not advised.** Sprawl refuses to read a secrets file that
is group- or world-readable and tells you to `chmod` it. On a host where several
agents run under one uid, that file is the credential to the authoritative
cross-host log.

**There is deliberately no config key for the DSN.** `.sprawl/config.yaml` is a
*tracked* file in a *public* repo, so a database credential must be structurally
unable to land in it. `SPRAWL_DB_DSN` and the secrets file are the only two
sources, and nothing in the codebase reads a third.

### 2. Enable the feature and create the schema

```bash
sprawl config set event_log.enabled true
sprawl store migrate
sprawl store status
```

`store migrate` is a **separate, privileged step** and not something a session
does on startup. That is a deliberate split: the application connects with a
least-privilege role that cannot migrate (see below), so a sprawl session that
tried to apply migrations would fail for every correctly-configured deployment.

## Least privilege, and why `events` is append-only

Migration `00002_m1a_app_role.sql` creates a `NOLOGIN` role `sprawl_app` holding
`SELECT` and `INSERT` on `events` — and nothing else. `UPDATE`, `DELETE` and
`TRUNCATE` are refused **by Postgres**, not by application code, so the log is
append-only even against a bug or a hostile client.

For that to mean anything, sprawl has to *connect* as a user inheriting the role:

```sql
CREATE ROLE sprawl_app_login LOGIN PASSWORD '…';
GRANT sprawl_app TO sprawl_app_login;
```

Then point `SPRAWL_DB_DSN` at `sprawl_app_login`, and run `store migrate` with a
separate privileged DSN (the owner).

**A DSN with owner or superuser credentials silently defeats all of this.** The
schema is identical, everything works, and history is rewritable. `sprawl store
doctor` checks the connection actually in use and says so:

```
append-only:  NOT ENFORCED
  store: the connection in use can modify or delete events, so the log is not append-only: this connection holds [UPDATE DELETE TRUNCATE] on events
next: point the DSN at a login role that inherits sprawl_app instead of one that owns the tables
```

It is a **warning, not a refusal** — the store must never be the reason an agent
cannot start. For a laptop, ignore it. For anything shared, don't.

## Degraded mode

When the database is unreachable, sprawl does not fail and does not hang:

- **Telemetry and lifecycle events spill** to
  `.sprawl/logs/ledger-spill/<date>.ndjson` (gitignored, 0600, pruned after 14
  days or 64 MiB). Replay is M1b; today the spill is a durable record with a
  `reason` on every line.
- **Contract events are refused, loudly**, with a next-action hint. A goal
  recorded only in a local file is invisible to every other host and to the
  sweeper, so it would read as work nobody is doing.
- **Agents keep running.** The store is checked once at startup, not per event —
  a dial timeout on every event would turn a database outage into a wedged fleet.

The trade-off to know: **degraded mode does not reconnect in place.** Once a
process has started degraded it stays degraded until restarted. Restart the
session after fixing the database.

## Checking it works

```bash
sprawl store status    # enabled? where did the DSN come from? connected or degraded?
sprawl store doctor    # + append-only verdict and spill backlog
```

Neither ever prints the DSN — they name its *source*. Transcripts get pasted
into issues.

Then look at the log with any SQL client:

```sql
SELECT s.name, e.at, e.payload
FROM events e JOIN event_type_schemas s ON s.id = e.schema_id
ORDER BY e.seq DESC LIMIT 20;
```

A session that has run at least one turn should show `run_started` and
`turn_finished`. There is deliberately no `sprawl store query`: agents get narrow
tools and never raw SQL, and an operator has psql.

## Running the tests

```bash
make test-store-pg                          # needs Docker; starts its own container
make test-e2e-matrix-store-pg-integration   # the same suite, but exits 77 (never 0)
                                            # when Docker is unavailable
```

`make validate` does **not** require Docker — the Docker-dependent suite is
behind the `store_pg` build tag and runs separately.
