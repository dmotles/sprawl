-- QUM-1249 (M1a): the least-privilege application role.
--
-- THIS FILE IS THE APPEND-ONLY GUARANTEE. `events` is append-only because
-- `sprawl_app` holds SELECT and INSERT on it and nothing else — not because any
-- Go code declines to issue an UPDATE. Application-level append-only is a
-- convention; a missing privilege is a refusal from the database.
--
-- Kept as its own migration so the whole privilege story is auditable in one
-- screen rather than buried at the end of the DDL.
--
-- OPERATIONAL PRECONDITION, and the one way this can be silently defeated: the
-- application must actually CONNECT as a user that inherits this role. A DSN
-- carrying owner or superuser credentials bypasses every REVOKE below while the
-- integration tests stay green, because they assume the role explicitly. That is
-- why store.VerifyAppendOnly re-checks the privilege at runtime against the
-- connection actually in use, and why `sprawl store doctor` reports its verdict.
--
-- The role is NOLOGIN: it carries privileges, not credentials. A deployment
-- grants it to whatever login user it provisions (QUM-1259), so no password
-- ever appears in this repo.

-- +goose Up

-- CREATE ROLE is CLUSTER-scoped while migrations are per-schema, so this MUST be
-- idempotent: without the guard, migrating a second schema on the same cluster
-- fails with "role already exists" — which surfaces as an unrelated-looking
-- setup failure in whichever test happens to run second.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sprawl_app') THEN
        CREATE ROLE sprawl_app NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

-- GRANT ... ON <table> needs a literal schema-qualified name, and this migration
-- may be applied into any schema (production: the default; tests: an isolated
-- per-test schema), so the grants are issued dynamically against
-- current_schema(). Hardcoding `public` would grant in the wrong place and leave
-- the role with no privileges at all wherever the tables actually live.
-- +goose StatementBegin
DO $$
DECLARE
    s text := quote_ident(current_schema());
BEGIN
    EXECUTE format('GRANT USAGE ON SCHEMA %s TO sprawl_app', s);

    -- events: APPEND ONLY. The REVOKE is belt-and-braces (nothing above granted
    -- these) and states the intent explicitly, so a future GRANT ALL added by
    -- someone skimming this file reads as the contradiction it is. TRUNCATE is
    -- named because it is a SEPARATE privilege that defeats append-only
    -- completely and is easy to omit from a mental model built on UPDATE/DELETE.
    EXECUTE format('GRANT SELECT, INSERT ON %s.events TO sprawl_app', s);
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %s.events FROM sprawl_app', s);
    EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %s.events FROM PUBLIC', s);

    -- open_contracts is a maintained projection: DELETE is REQUIRED here, in the
    -- same transaction as a close-event append. Tightening this to match
    -- events' append-only shape would break every close.
    EXECUTE format('GRANT SELECT, INSERT, DELETE ON %s.open_contracts TO sprawl_app', s);

    -- Definitions are published by humans, never by agents: read-only.
    EXECUTE format('GRANT SELECT ON %s.event_type_schemas TO sprawl_app', s);
    EXECUTE format('GRANT SELECT ON %s.agent_cards TO sprawl_app', s);
    EXECUTE format('GRANT SELECT ON %s.workflow_defs TO sprawl_app', s);

    -- projects and artifacts are append-and-read: a first enable registers the
    -- project, and artifacts are content-addressed, so an existing row is
    -- reused rather than rewritten.
    EXECUTE format('GRANT SELECT, INSERT ON %s.projects TO sprawl_app', s);
    EXECUTE format('GRANT SELECT, INSERT ON %s.artifacts TO sprawl_app', s);

    -- Mutable state: an instance is closed (closed_at) and a session's
    -- last_turn_boundary advances, so both need UPDATE.
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %s.workflow_instances TO sprawl_app', s);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %s.agent_sessions TO sprawl_app', s);

    -- event_claims (M1b): claim, renew a lease, release. All four verbs.
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %s.event_claims TO sprawl_app', s);
END
$$;
-- +goose StatementEnd

-- +goose Down

-- The role is deliberately NOT dropped: it is cluster-scoped and may be granted
-- to login users and hold privileges in other schemas, so dropping it while
-- rolling back one schema would break the others. Only this schema's grants go.
-- +goose StatementBegin
DO $$
DECLARE
    s text := quote_ident(current_schema());
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sprawl_app') THEN
        EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %s FROM sprawl_app', s);
        EXECUTE format('REVOKE USAGE ON SCHEMA %s FROM sprawl_app', s);
    END IF;
END
$$;
-- +goose StatementEnd
