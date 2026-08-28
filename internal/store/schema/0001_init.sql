-- Opportunity Bridge — initial schema.
--
-- Every statement is idempotent, and every migration in this directory is run
-- on every start. There is no "current version" to query and no tracking table
-- to fall out of step with reality: the database's own contents are the truth,
-- and a statement that has already been applied is a no-op. A later change is a
-- new, higher-numbered file containing ALTERs guarded the same way; this file
-- is never edited once it has been applied anywhere.
--
-- Shape: one table per aggregate root, with the columns anything queries or
-- orders by promoted out of the document, and the rest of the aggregate kept in
-- `doc`. Fully normalising the turns of a conversation would buy nothing here —
-- nothing queries inside them — while costing a join on the hot path and a
-- migration every time a field moves.

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    subject_id TEXT        NOT NULL,
    role       TEXT        NOT NULL,
    locale     TEXT        NOT NULL DEFAULT '',
    intent     TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    doc        JSONB       NOT NULL
);
-- The conversation picker orders by last activity and scopes by owner.
CREATE INDEX IF NOT EXISTS sessions_subject_idx ON sessions (subject_id);
CREATE INDEX IF NOT EXISTS sessions_updated_idx ON sessions (updated_at DESC);

CREATE TABLE IF NOT EXISTS profiles (
    subject_id TEXT PRIMARY KEY,
    doc        JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS case_tasks (
    id         TEXT PRIMARY KEY,
    subject_id TEXT        NOT NULL,
    status     TEXT        NOT NULL,
    doc        JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS case_tasks_subject_idx ON case_tasks (subject_id);

-- Consent is keyed by (subject, scope) because that pair IS the grant: a second
-- row for the same pair would be a second answer to one question.
CREATE TABLE IF NOT EXISTS consent (
    subject_id TEXT        NOT NULL,
    scope      TEXT        NOT NULL,
    granted    BOOLEAN     NOT NULL,
    granted_at TIMESTAMPTZ,
    note       TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (subject_id, scope)
);

CREATE TABLE IF NOT EXISTS approvals (
    id         TEXT PRIMARY KEY,
    session_id TEXT        NOT NULL,
    decided    BOOLEAN     NOT NULL,
    doc        JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS approvals_session_idx ON approvals (session_id);

-- Demand signals are an append-only list with no natural key of their own, so
-- the ordinal is the identity. `ord` is assigned by the writer from the
-- snapshot's own ordering rather than by a sequence, so that re-syncing the
-- same list does not renumber it.
CREATE TABLE IF NOT EXISTS demand_signals (
    ord INTEGER PRIMARY KEY,
    doc JSONB NOT NULL
);

-- username is the primary key in its NORMALISED form (see NormaliseUsername):
-- "Damon" and "damon" must be one account, and the database is the last place
-- that can still be true if the application forgets.
CREATE TABLE IF NOT EXISTS accounts (
    username   TEXT PRIMARY KEY,
    subject_id TEXT        NOT NULL UNIQUE,
    doc        JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS signins (
    token_hash TEXT PRIMARY KEY,
    username   TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS signins_expires_idx ON signins (expires_at);

-- Scalars that belong to the store as a whole: the id sequence, and the marker
-- that says the pre-accounts adoption has run. They live in one table rather
-- than one table each because they are not aggregates and never will be.
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value JSONB NOT NULL
);
