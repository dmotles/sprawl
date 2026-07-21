-- +goose Up
-- +goose StatementBegin

-- users: the single principal. EXACTLY ONE ROW is enforced by the pairing of a
-- CHECK pinning `singleton` to TRUE and a UNIQUE constraint on it: a second
-- distinct row can only carry singleton=TRUE, which the unique index rejects.
-- user_id is constant everywhere in the single-user MVP (docs 07 §0).
CREATE TABLE users (
    user_id    TEXT PRIMARY KEY,
    singleton  BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_singleton_ck CHECK (singleton = TRUE),
    CONSTRAINT users_singleton_uq UNIQUE (singleton)
);

-- tokens: HASHED bearer token(s) only — never plaintext (docs 07 §0). token_id
-- is an opaque row id, NOT the secret.
CREATE TABLE tokens (
    token_id   TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(user_id),
    token_hash BYTEA NOT NULL,
    label      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX tokens_hash_uq ON tokens (token_hash);
CREATE INDEX tokens_user_active_idx ON tokens (user_id) WHERE revoked_at IS NULL;

-- hosts: per machine/install. last_run_id is the most recent RegisterInstance
-- run_id.
CREATE TABLE hosts (
    host_id       TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(user_id),
    repo_label    TEXT NOT NULL DEFAULT '',
    last_run_id   TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX hosts_user_idx ON hosts (user_id);

-- projects: per repo/project; the active-host + memory scope.
CREATE TABLE projects (
    project_id TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(user_id),
    repo_label TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX projects_user_idx ON projects (user_id);

-- active_host: advisory marker (docs 07 §3). ONE row per project, upsert. No
-- fence, no lease, no epoch column — by design.
CREATE TABLE active_host (
    project_id   TEXT PRIMARY KEY REFERENCES projects(project_id),
    user_id      TEXT NOT NULL REFERENCES users(user_id),
    host_id      TEXT NOT NULL REFERENCES hosts(host_id),
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX active_host_host_idx ON active_host (host_id);

-- sessions: one per `sprawl enter`; owns a seq space. head_seq is the stream
-- high-water; the session_stream table itself lands in P0-4.
CREATE TABLE sessions (
    session_id TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(user_id),
    host_id    TEXT NOT NULL REFERENCES hosts(host_id),
    project_id TEXT REFERENCES projects(project_id),
    run_id     TEXT NOT NULL DEFAULT '',
    head_seq   BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at   TIMESTAMPTZ
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_host_idx ON sessions (host_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS active_host;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS hosts;
DROP TABLE IF EXISTS tokens;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
