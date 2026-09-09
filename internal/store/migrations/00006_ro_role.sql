-- QUM-1349 (web UI, slice A): the read-only role the UI API connects as.
--
-- `sprawl_ro` is SELECT and nothing else, on every table in the event-log
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
-- the role explicitly with SET ROLE. Deployment (QUM-1350) creates the LOGIN
-- user `sprawl_ui_login` and grants it this role; that user is provisioned with
-- admin credentials at deploy time and its password never enters this repo,
-- which is why no CREATE ROLE for it appears below.
--
-- The name is `sprawl_ro`, not `sprawl_ui_ro`: the role holds a generic
-- SELECT-only grant set, so a second read-only consumer inherits it rather than
-- acquiring a near-duplicate role. What is UI-specific is the login user.

-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sprawl_ro') THEN
        CREATE ROLE sprawl_ro NOLOGIN;
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
-- resembling a missing grant.
--
-- The FOR ROLE clause is the whole point, and getting it wrong fails by NO-OP
-- with no error to notice. Default privileges attach to a GRANTOR, not to a
-- schema: an entry only fires for tables that THAT role goes on to create.
--
-- So the clause is issued once per plausible future creator, because neither
-- candidate is sufficient alone and each fails silently:
--
--   * current_user — whoever runs `sprawl store migrate`, and therefore whoever
--     creates the tables a later migration adds. This is the common case and
--     the one an unqualified statement would have covered anyway.
--   * the schema owner, when it differs — a production migrate run by an admin
--     that is not the owner leaves the owner's future tables uncovered, which
--     is the case an unqualified statement gets wrong.
--
-- pg_database_owner is EXCLUDED, and it is the trap that motivated writing this
-- as a loop. In the `public` schema of a modern Postgres, nspowner is the
-- pseudo-role `pg_database_owner`, so resolving "the owner" from the catalogue
-- and stopping there produces an entry granted BY a role that creates nothing.
-- Measured, not reasoned about: with only that entry present, a table created
-- by the migrating user immediately after `store migrate` was unreadable to
-- sprawl_ro with `permission denied`, while `pg_default_acl` showed a
-- perfectly healthy-looking `sprawl_ro=r/pg_database_owner` row.
-- +goose StatementBegin
DO $$
DECLARE
    s text := quote_ident(current_schema());
    grantor text;
    t text;
BEGIN
    EXECUTE format('GRANT USAGE ON SCHEMA %s TO sprawl_ro', s);

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
        EXECUTE format('GRANT SELECT ON %s.%I TO sprawl_ro', s, t);
        -- Belt-and-braces, and it states the intent explicitly so that a future
        -- GRANT ALL added by someone skimming this file reads as the
        -- contradiction it is. TRUNCATE is named because it is a SEPARATE
        -- privilege that is easy to omit from a mental model built on
        -- UPDATE/DELETE, and it destroys the log outright.
        EXECUTE format('REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON %s.%I FROM sprawl_ro', s, t);
    END LOOP;

    FOR grantor IN
        SELECT DISTINCT quote_ident(r)
        FROM unnest(ARRAY[
            current_user::text,
            (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = current_schema())
        ]) AS r
        WHERE r IS NOT NULL AND r <> 'pg_database_owner'
    LOOP
        EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT ON TABLES TO sprawl_ro', grantor, s);
    END LOOP;
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
    -- Revoked per GRANTOR, from the catalogue rather than from the Up leg's
    -- candidate list: a revoke naming a role the Up did not grant for targets
    -- an entry that does not exist and leaves the real one in place, so a
    -- rolled-back schema would keep silently re-granting SELECT on every table
    -- created in it afterwards.
    grantor text;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sprawl_ro') THEN
        FOR grantor IN
            SELECT quote_ident(pg_get_userbyid(d.defaclrole))
            FROM pg_default_acl d
            JOIN pg_namespace n ON n.oid = d.defaclnamespace
            WHERE n.nspname = current_schema() AND d.defaclobjtype = 'r'
        LOOP
            EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s REVOKE SELECT ON TABLES FROM sprawl_ro', grantor, s);
        END LOOP;
        EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %s FROM sprawl_ro', s);
        EXECUTE format('REVOKE USAGE ON SCHEMA %s FROM sprawl_ro', s);
    END IF;
END
$$;
-- +goose StatementEnd
