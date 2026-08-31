-- Outreach is the recruiter/candidate contact handshake added with the
-- recruiter role. See docs/16-recruiter-and-outreach.md.
--
-- It is a table of its own rather than a column on profiles or a case_task
-- because it is the only record in this service with TWO parties: it is written
-- by one person and answered by another, and either side can list their own half
-- of it. Folded into case_tasks it would have needed a second subject column
-- that means nothing for every other row in that table.
--
-- Idempotent like every other file here: applied on every start, first or
-- thousandth. See pgBackend.migrate.
CREATE TABLE IF NOT EXISTS outreach (
    id           TEXT PRIMARY KEY,
    subject_id   TEXT        NOT NULL,
    recruiter_id TEXT        NOT NULL,
    status       TEXT        NOT NULL,
    doc          JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL
);

-- Both sides list their own requests, so both directions are indexed. The
-- candidate's lookup is the one on a user-facing path (the "somebody is waiting
-- on you" badge), and it runs on every load of the overview panel.
CREATE INDEX IF NOT EXISTS outreach_subject_idx ON outreach (subject_id);
CREATE INDEX IF NOT EXISTS outreach_recruiter_idx ON outreach (recruiter_id);
