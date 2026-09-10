-- Lead Graph, first schema. See docs/20-lead-graph.zh-CN.md.
--
-- IDEMPOTENT, like every file applied here: it runs on every start, first or
-- thousandth, and the second run must succeed. A migration guarded only by a
-- tracking table is a claim ABOUT the schema; the schema itself is the truth.
--
-- WHY NODES AND EDGES ARE TWO TABLES AND NOT ONE POLYMORPHIC ONE
--   Edges have two endpoints and a validity window; nodes have neither. Folded
--   together, every node row would carry three columns that mean nothing for it,
--   and the foreign keys that keep an edge's ends honest could not exist.
--
-- WHY doc JSONB RATHER THAN A COLUMN PER FIELD
--   The kinds differ in which fields they carry (a person has a duty, an event
--   has a date) and the set is still moving. The columns promoted out of the
--   document are exactly the ones queried or constrained: team, kind, the
--   endpoints, the natural key, and the validity window.
--
-- WHY ANNOTATIONS ARE A THIRD TABLE (拍板 2026-09-10, Q1)
--   Facts belong to the team; judgements belong to the seat. Keeping a private
--   note in a column on the shared row would make "your teammates cannot see it"
--   a promise enforced by every SELECT remembering to exclude it. Here it is a
--   different row, keyed by seat, and the join simply is not made. It is also
--   what makes a departing consultant a one-row-set delete rather than a
--   column-by-column audit.

CREATE TABLE IF NOT EXISTS lead_nodes (
    id          TEXT PRIMARY KEY,
    team_id     TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    natural_key TEXT        NOT NULL,
    doc         JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL
);

-- The same fact arriving twice is an update, not a duplicate. Enforced here as
-- well as in the store so that a second writer - a backfill, an import - cannot
-- produce the duplicate the store would have refused. The key already contains
-- the team id, so this is unique per team.
CREATE UNIQUE INDEX IF NOT EXISTS lead_nodes_key_idx ON lead_nodes (natural_key);
CREATE INDEX IF NOT EXISTS lead_nodes_team_idx ON lead_nodes (team_id, kind);

CREATE TABLE IF NOT EXISTS lead_edges (
    id          TEXT PRIMARY KEY,
    team_id     TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    natural_key TEXT        NOT NULL,
    from_id     TEXT        NOT NULL REFERENCES lead_nodes (id) ON DELETE CASCADE,
    to_id       TEXT        NOT NULL REFERENCES lead_nodes (id) ON DELETE CASCADE,
    valid_until TIMESTAMPTZ,
    doc         JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL
);

-- ON DELETE CASCADE above is what makes "delete is real" true at the storage
-- layer: forgetting a person cannot leave dangling lines that still name them.
CREATE UNIQUE INDEX IF NOT EXISTS lead_edges_key_idx ON lead_edges (natural_key);
CREATE INDEX IF NOT EXISTS lead_edges_team_idx ON lead_edges (team_id, kind);
CREATE INDEX IF NOT EXISTS lead_edges_from_idx ON lead_edges (team_id, from_id);
CREATE INDEX IF NOT EXISTS lead_edges_to_idx ON lead_edges (team_id, to_id);

-- One seat's private overlay: how well they know somebody, and what they noted.
-- target_id is a node id or an edge id; the two cascade rules below are why it
-- is not a foreign key to either - a row is deleted explicitly when its target
-- goes, in the same transaction, so that a deletion request is answerable.
-- Like every other table here it carries the record as a document, with the
-- columns that are queried or constrained promoted out of it. Uniform on
-- purpose: one generic upsert writes all eight, so adding a record type is a
-- call site rather than another hand-written statement to get subtly wrong.
CREATE TABLE IF NOT EXISTS lead_annotations (
    team_id    TEXT        NOT NULL,
    seat_id    TEXT        NOT NULL,
    target_id  TEXT        NOT NULL,
    strength   SMALLINT,
    note       TEXT,
    doc        JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (seat_id, target_id)
);

-- Seat-first, because the two reads are "everything this seat annotated"
-- (projection on every graph read) and "drop this seat" (they left).
CREATE INDEX IF NOT EXISTS lead_annotations_target_idx ON lead_annotations (target_id);

-- Contact records. A third record type rather than a node because a contact has
-- no name and no identity of its own: it is identified by who, when and by whom.
-- Folding it into lead_nodes would have made natural_key mean two different
-- things depending on kind, and that key is what every idempotent write rests on.
--
-- team_id, not seat_id, is the visibility boundary: "somebody already called him
-- last week" is exactly the fact that stops two consultants cold-calling the same
-- person, and it has to survive the one who made the call leaving. Only the
-- private impression on it lives in lead_annotations.
CREATE TABLE IF NOT EXISTS lead_touchpoints (
    id          TEXT PRIMARY KEY,
    team_id     TEXT        NOT NULL,
    seat_id     TEXT        NOT NULL,
    person_id   TEXT        NOT NULL REFERENCES lead_nodes (id) ON DELETE CASCADE,
    natural_key TEXT        NOT NULL,
    at          TIMESTAMPTZ NOT NULL,
    via         TEXT        NOT NULL,
    due_at      TIMESTAMPTZ,
    doc         JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

-- The same call mentioned twice is one record.
CREATE UNIQUE INDEX IF NOT EXISTS lead_touchpoints_key_idx ON lead_touchpoints (natural_key);
-- "has anybody spoken to him lately" - the read on every path result.
CREATE INDEX IF NOT EXISTS lead_touchpoints_person_idx ON lead_touchpoints (team_id, person_id, at DESC);
-- "what do I owe this week" - this seat's own follow-ups.
CREATE INDEX IF NOT EXISTS lead_touchpoints_due_idx ON lead_touchpoints (seat_id, due_at);

-- The source ledger: one row per fetched document, kept whether or not it
-- produced anything. "We looked and there was nothing" and "we never looked"
-- are different answers, and only this table can tell them apart.
--
-- digest is part of the key, not just a column: the same URL returning changed
-- content becomes a NEW row rather than overwriting the old one, which is how a
-- quiet edit at the source stays visible.
CREATE TABLE IF NOT EXISTS lead_observations (
    id          TEXT PRIMARY KEY,
    team_id     TEXT        NOT NULL,
    source      TEXT        NOT NULL,
    url         TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    org         TEXT,
    unit        TEXT,
    digest      TEXT        NOT NULL,
    natural_key TEXT        NOT NULL,
    posted_at   TIMESTAMPTZ,
    at          TIMESTAMPTZ NOT NULL,
    doc         JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS lead_observations_key_idx ON lead_observations (natural_key);
-- "how long since this group advertised" - the hiring-gap read.
CREATE INDEX IF NOT EXISTS lead_observations_unit_idx ON lead_observations (team_id, org, unit, kind, at DESC);

-- Alerts are team-wide: a reorganisation at a company matters to everybody who
-- works that company, not only to whoever's daily pass noticed it. There is
-- deliberately NO score or rank column - ordering by "likelihood" would be the
-- departure-probability ranking this product refuses to build.
CREATE TABLE IF NOT EXISTS lead_alerts (
    id         TEXT PRIMARY KEY,
    team_id    TEXT        NOT NULL,
    event_id   TEXT        NOT NULL REFERENCES lead_nodes (id) ON DELETE CASCADE,
    ack        BOOLEAN     NOT NULL DEFAULT FALSE,
    doc        JSONB       NOT NULL,
    raised_at  TIMESTAMPTZ NOT NULL
);

-- One event, one alert, ever. A repeated alert is a nudge to act, and this
-- feature exists so nothing is missed - not to push anybody into a call.
CREATE UNIQUE INDEX IF NOT EXISTS lead_alerts_event_idx ON lead_alerts (team_id, event_id);
CREATE INDEX IF NOT EXISTS lead_alerts_open_idx ON lead_alerts (team_id, ack, raised_at DESC);

-- What a regulator, a customer's legal team, or a leak investigation would ask
-- about: who took a copy, who asked for their data, what we refused to fetch.
CREATE TABLE IF NOT EXISTS lead_audit (
    id      TEXT        PRIMARY KEY,
    team_id TEXT        NOT NULL,
    seat_id TEXT        NOT NULL,
    action  TEXT        NOT NULL,
    at      TIMESTAMPTZ NOT NULL,
    doc     JSONB       NOT NULL
);

CREATE INDEX IF NOT EXISTS lead_audit_team_idx ON lead_audit (team_id, at DESC);

-- Questions waiting on a person. These outlive a restart on purpose: a backlog
-- that evaporates when the process recycles is worse than no backlog, because
-- the user was told the question was saved.
CREATE TABLE IF NOT EXISTS lead_pending (
    id         TEXT PRIMARY KEY,
    team_id    TEXT        NOT NULL,
    seat_id    TEXT        NOT NULL,
    dedupe_key TEXT        NOT NULL,
    doc        JSONB       NOT NULL,
    at         TIMESTAMPTZ NOT NULL
);

-- One question per seat per thing being asked about; repeating yourself does
-- not queue it twice.
CREATE UNIQUE INDEX IF NOT EXISTS lead_pending_key_idx ON lead_pending (seat_id, dedupe_key);
CREATE INDEX IF NOT EXISTS lead_pending_seat_idx ON lead_pending (team_id, seat_id, at);

-- Who may use this graph. A seat is the unit of identity: it belongs to exactly
-- one team, it owns its own private annotations, and its token is what turns an
-- HTTP request into a View.
--
-- The token is stored HASHED. A token database that can be read back is a
-- credential database, and the point of a token is that only its holder has it.
CREATE TABLE IF NOT EXISTS lead_seats (
    seat_id    TEXT PRIMARY KEY,
    team_id    TEXT        NOT NULL,
    label      TEXT        NOT NULL,
    token_hash TEXT        NOT NULL,
    doc        JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

-- The lookup on every single request.
CREATE UNIQUE INDEX IF NOT EXISTS lead_seats_token_idx ON lead_seats (token_hash);
CREATE INDEX IF NOT EXISTS lead_seats_team_idx ON lead_seats (team_id);
