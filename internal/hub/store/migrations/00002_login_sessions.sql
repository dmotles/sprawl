-- +goose Up
-- +goose StatementBegin

-- login_sessions: browser login sessions minted by /login (docs 04 §6).
--
-- QUM-878 Decision 1: this is a DEDICATED table, NOT the enter `sessions` table
-- from migration 00001. That table has `host_id TEXT NOT NULL REFERENCES hosts`
-- and is shaped for a `sprawl enter` session — a browser login has no host, so
-- reusing it would force a fake host FK and perturb enter-session semantics. A
-- separate table keeps the two concerns cleanly apart.
--
-- The cookie carries an opaque session_id; the server verifies its HMAC
-- signature and then looks up this row on every use. expires_at bounds the
-- session; expiry is enforced in the auth layer. Revocation = DELETE the row
-- (logout / admin), which invalidates the cookie on its next use. user_id is
-- always the single MVP user now (docs 04 §3) — present so multi-user is a data
-- change, not a schema migration.
CREATE TABLE login_sessions (
    session_id TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(user_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX login_sessions_user_idx ON login_sessions (user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS login_sessions;
-- +goose StatementEnd
