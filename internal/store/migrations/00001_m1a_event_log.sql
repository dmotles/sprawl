-- QUM-1249 (M1a): the event-log core schema.
--
-- This is Appendix A of docs/designs/v2-log-centric-rearchitecture.md, scoped to
-- M1a. Appendix A is BINDING; where this file adds something the sketch does not
-- spell out (NOT NULLs, index names, the payload size CHECK) the addition is
-- commented with its reason.
--
-- DELIBERATELY ABSENT, and not an oversight:
--   * event_embeddings + the HNSW index — M4. Consequently this migration does
--     NOT `CREATE EXTENSION vector`: with no vector column there is nothing for
--     it to serve, and requiring it would add a hard dependency (and a container
--     image pull) for a table this milestone does not create. The code works
--     against any Postgres >= 16, with or without pgvector installed.
--   * entities / facts / fact_provenance — M6 (memory).
--
-- event_claims IS created here and is READ BY NOTHING in M1a. That is
-- deliberate: M1b's dispatcher builds on it, and its composite primary key is
-- Appendix B item 1's exactly-once guarantee. Creating it now means M1b does not
-- ship a migration and a dispatcher in the same diff.

-- +goose Up

-- projects: identity is the repo remote URL.
CREATE TABLE projects (
    id         uuid PRIMARY KEY,
    remote_url text UNIQUE NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Immutable definitions: one pattern, three tables. (name, version) is the
-- identity; rows are referenced BY ID, never by name, so a pinned reference
-- keeps meaning something after a bump.
--
-- `closes` holds the NAME of the opener this type closes (not an id) because a
-- close type closes every version of its opener.
CREATE TABLE event_type_schemas (
    id          uuid PRIMARY KEY,
    name        text NOT NULL,
    version     int NOT NULL,
    json_schema jsonb NOT NULL,
    closes      text,
    opens       boolean NOT NULL DEFAULT false,
    supersedes  uuid REFERENCES event_type_schemas(id),
    UNIQUE (name, version)
);

CREATE TABLE agent_cards (
    id      uuid PRIMARY KEY,
    name    text NOT NULL,
    version int NOT NULL,
    prompt  text,
    tools   jsonb,
    model   text,
    effort  text,
    rubric  jsonb,
    budgets jsonb,
    UNIQUE (name, version)
);

CREATE TABLE workflow_defs (
    id                   uuid PRIMARY KEY,
    name                 text NOT NULL,
    version              int NOT NULL,
    trigger_event_schema uuid REFERENCES event_type_schemas(id),
    steps                jsonb NOT NULL,
    UNIQUE (name, version)
);

-- artifacts: the fat half of the thin-event/fat-artifact split.
-- Content-addressed: sha256 UNIQUE is what makes dedup real rather than
-- aspirational, and what stops the same report existing under two ids.
CREATE TABLE artifacts (
    id           uuid PRIMARY KEY,
    kind         text NOT NULL,
    content      text NOT NULL,
    base_git_sha text,
    size_bytes   int,
    sha256       bytea UNIQUE
);

-- events: append-only. "Append-only" is ENFORCED BY GRANTS, not by anything in
-- this file — see 00002_m1a_app_role.sql. A trigger would be bypassable by
-- whoever can drop the trigger; a missing UPDATE privilege is not.
--
-- `seq` is the global total order the entire design rests on. GENERATED ALWAYS
-- (not BY DEFAULT) so a caller cannot supply its own value and forge ordering.
CREATE TABLE events (
    seq                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id                   uuid UNIQUE NOT NULL,
    project_id           uuid NOT NULL REFERENCES projects(id),
    workflow_instance_id uuid NOT NULL,
    schema_id            uuid NOT NULL REFERENCES event_type_schemas(id),
    agent_session_id     uuid,
    owner_agent_id       uuid,
    closes_event_id      uuid REFERENCES events(id),
    payload              jsonb NOT NULL,
    artifact_id          uuid REFERENCES artifacts(id),
    at                   timestamptz NOT NULL DEFAULT now(),
    -- The plan's "thin events (<= ~8KB), fat artifacts" policy, enforced HERE
    -- rather than in Go. A Go-only bound is bypassed by any direct psql insert
    -- or backfill script, and this is the kind of invariant that rots the first
    -- time someone writes one. octet_length over the text rendering rather than
    -- pg_column_size: the latter reports the possibly-compressed stored size, so
    -- a fat-but-compressible payload would slip through.
    CONSTRAINT events_payload_thin_ck CHECK (octet_length(payload::text) <= 8192)
);

-- The two read paths Appendix A names. Both are ordered by seq, so an index on
-- the leading column alone would not serve them.
CREATE INDEX events_workflow_instance_seq_idx ON events (workflow_instance_id, seq);
CREATE INDEX events_project_seq_idx ON events (project_id, seq);

CREATE TABLE workflow_instances (
    id                 uuid PRIMARY KEY,
    project_id         uuid REFERENCES projects(id),
    workflow_def_id    uuid REFERENCES workflow_defs(id),
    triggered_by_event uuid,
    started_at         timestamptz NOT NULL DEFAULT now(),
    closed_at          timestamptz,
    budget_tokens      bigint,
    budget_usd         numeric
);

-- agent_sessions is an ADVISORY PROJECTION. The local on-disk AgentState
-- taxonomy remains the sole wake arbiter; nothing may derive a wake decision
-- from this table.
CREATE TABLE agent_sessions (
    id                 uuid PRIMARY KEY,
    card_id            uuid REFERENCES agent_cards(id),
    host               text,
    last_turn_boundary timestamptz,
    status             text
);

-- open_contracts: a DERIVED, REBUILDABLE projection — insert on an opens-typed
-- append, delete on a close, in the same transaction as the append. It is a
-- BASE TABLE and not a view over the anti-join on purpose: a view would make
-- every outstanding-work read a full anti-join, and the drop/rebuild test
-- exists precisely to prove the maintained table agrees with that anti-join.
CREATE TABLE open_contracts (
    event_id             uuid PRIMARY KEY REFERENCES events(id),
    owner_agent_id       uuid,
    workflow_instance_id uuid,
    opened_at            timestamptz NOT NULL DEFAULT now()
);

-- event_claims: exactly-one-actor-per-event dispatch claims (M1b).
-- PRIMARY KEY (event_id, consumer) IS the mechanism: `INSERT ... ON CONFLICT DO
-- NOTHING` against this key is what makes a crash-and-redeliver idempotent
-- instead of double-spawning.
CREATE TABLE event_claims (
    event_id      uuid NOT NULL REFERENCES events(id),
    consumer      text NOT NULL,
    host          text,
    claimed_at    timestamptz NOT NULL DEFAULT now(),
    lease_expires timestamptz,
    PRIMARY KEY (event_id, consumer)
);

-- +goose Down

DROP TABLE event_claims;
DROP TABLE open_contracts;
DROP TABLE agent_sessions;
DROP TABLE workflow_instances;
DROP TABLE events;
DROP TABLE artifacts;
DROP TABLE workflow_defs;
DROP TABLE agent_cards;
DROP TABLE event_type_schemas;
DROP TABLE projects;
