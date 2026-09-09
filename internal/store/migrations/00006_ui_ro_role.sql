-- QUM-1349 (web UI, slice A): the read-only role the UI API connects as.
--
-- `sprawl_ui_ro` is SELECT and nothing else, on every table in the event-log
-- schema. It is the DATABASE's refusal, not the API's: the read-only v1 ships
-- with no auth and is browser-reachable, so "the handlers only issue SELECTs"
-- is a convention that one future handler defeats, while a missing privilege is
-- a refusal nothing in Go can talk its way past.
--
-- Deliberately a SECOND role rather than a reuse of `sprawl_app`. sprawl_app is
-- the agent fleet's identity and holds INSERT on `events`; the UI's identity is
-- reachable from a browser. Keeping them separate keeps the audit trail
-- separable and is worth a duplicated grant block. A later read-WRITE UI role
-- (`sprawl_ui_rw`) is a third role, not a widening of this one.
--
-- Mirrors 00002_m1a_app_role.sql throughout — NOLOGIN, pg_roles-guarded,
-- dynamic current_schema() grants, not dropped on Down. Read that file's header
-- for why each of those is the way it is; the reasoning is identical and is not
-- repeated here.
--
-- OPERATIONAL PRECONDITION, same as 00002's: the API must actually CONNECT as a
-- login user that inherits this role. A DSN carrying owner credentials makes
-- every REVOKE below decorative while the tests stay green, because they assume
-- the role explicitly with SET ROLE. Deployment (QUM-1350) grants this role to
-- a login user whose password never enters this repo.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sprawl_ui_ro') THEN
        CREATE ROLE sprawl_ui_ro NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

-- The grants are ENUMERATED PER TABLE rather than issued as
-- `GRANT SELECT ON ALL TABLES IN SCHEMA` — the same deliberate choice 00002
-- makes, and for the same reason, which survives the fact that the enumeration
-- currently covers every table there is: `ON ALL TABLES` is a statement about
-- whatever happens to exist when it runs, so the privilege set becomes a
-- function of migration order rather than of anything written down. Adding a
-- table then silently widens this role with nobody deciding to, and the diff
-- that did it touches no file that mentions grants. Enumerated, a new table is
-- invisible to the UI until someone adds a line here, which is the review
-- conversation that should happen.
--
-- ALTER DEFAULT PRIVILEGES is the escape hatch for exactly one hazard the
-- enumeration creates and cannot fix: a table added by a LATER migration is
-- unreadable, and the symptom is a 42501 from a handler rather than anything
-- resembling a missing grant. It applies only to tables created by the role
-- running this migration (the schema owner), which is who creates them.
-- +goose StatementBegin
DO $$
DECLARE
    s text := quote_ident(current_schema());
    t text;
BEGIN
    EXECUTE format('GRANT USAGE ON SCHEMA %s TO sprawl_ui_ro', s);

    -- Every table in the event-log schema, named one at a time. The whole
    -- schema is readable on purpose: the UI's job is visibility, and the views
    -- need the definition tables (event_type_schemas resolves an event's type
    -- name, agent_cards its model) as much as the ledger itself. What is
    -- withheld is every WRITING verb, not any particular table.
    FOR t IN SELECT unnest(ARRAY[
        'projects',
        'event_type_schemas',
        'agent_cards',
        'workflow_defs',
        'artifacts',
        'events',
        'workflow_instances',
        'agent_sessions',
        'open_contracts',
        'event_claims'
    ])
    LOOP
        EXECUTE format('GRANT SELECT ON %s.%I TO sprawl_ui_ro', s, t);
        -- Belt-and-braces, and it states the intent explicitly so that a future
        -- GRANT ALL added by someone skimming this file reads as the
        -- contradiction it is. TRUNCATE is named because it is a SEPARATE
        -- privilege that is easy to omit from a mental model built on
        -- UPDATE/DELETE, and it destroys the log outright.
        EXECUTE format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %s.%I FROM sprawl_ui_ro', s, t);
    END LOOP;

    EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT ON TABLES TO sprawl_ui_ro', s);
END
$$;
-- +goose StatementEnd

-- +goose Down

-- The role is deliberately NOT dropped, for 00002's reason: it is
-- cluster-scoped and may be granted to login users and hold privileges in other
-- schemas, so dropping it while rolling back one schema would break the others.
-- Only this schema's grants go — including the default privileges, which
-- otherwise outlive the rollback and silently re-grant SELECT on the next table
-- created here.
-- +goose StatementBegin
DO $$
DECLARE
    s text := quote_ident(current_schema());
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sprawl_ui_ro') THEN
        EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %s REVOKE SELECT ON TABLES FROM sprawl_ui_ro', s);
        EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %s FROM sprawl_ui_ro', s);
        EXECUTE format('REVOKE USAGE ON SCHEMA %s FROM sprawl_ui_ro', s);
    END IF;
END
$$;
-- +goose StatementEnd
