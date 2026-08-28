#!/usr/bin/env bash
# Create the `oba` role and database on the shared postgres instance.
#
# WHY A SEPARATE DATABASE, NOT TABLES IN `heros`
#   Opportunity Bridge shares the postgres INSTANCE that the heros platform runs
#   on, because there is one node and one budget. It does not share the
#   DATABASE: a shared database means a shared migration chain and a shared
#   restore blast radius, so restoring heros to last night would silently roll
#   this product back with it. Separate database, separate role, same instance.
#
# WHY THE PASSWORD IS FETCHED HERE RATHER THAN PASSED IN
#   This runs on the k3s node over SSM. deploy/deploy.sh states the rule a
#   credential must never appear in a manifest, a command line or an SSM
#   invocation, and an SSM payload is retained and readable after the fact. The
#   node's own IAM role already grants read on opportunity-bridge/* and nothing
#   more, so the DSN is read at run time and never travels in the request.
#
# WHY `REVOKE CONNECT ... FROM PUBLIC` AND NOT `FROM oba`
#   Postgres grants CONNECT on every database to PUBLIC by default, and every
#   role inherits it. Revoking from `oba` alone therefore does NOTHING — measured
#   on 2026-08-28, `oba` could still open the heros database and enumerate its
#   191 tables (it could read no rows: no table-level SELECT was ever granted).
#   The grant has to be taken off PUBLIC and handed back to the owner explicitly.
#   This is the whole reason this file exists rather than a couple of ad-hoc
#   psql commands: the obvious REVOKE is the wrong one, and it fails silently.
#
# Idempotent: running it twice lands the same state as running it once.
# Requires: kubectl and aws on the host, and a postgres-0 pod in `heros`.
set -euo pipefail

REGION="${REGION:-us-east-1}"
SECRET="${SECRET:-opportunity-bridge/db}"
PG_NS="${PG_NS:-heros}"
PG_POD="${PG_POD:-postgres-0}"
PG_SUPERUSER="${PG_SUPERUSER:-heros}"
DRY_RUN=0
[ "${1:-}" = "--dry-run" ] && DRY_RUN=1

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

say "reading the DSN from ${SECRET}"
DSN=$(aws secretsmanager get-secret-value --region "$REGION" --secret-id "$SECRET" \
  --query SecretString --output text \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["OBA_DATABASE_URL"])')

# Parsed rather than assumed, so the role and database this creates are always
# exactly the ones the application will later connect as.
DB_USER=$(printf '%s' "$DSN" | sed -E 's|^postgres://([^:]+):.*$|\1|')
DB_PASS=$(printf '%s' "$DSN" | sed -E 's|^postgres://[^:]+:([^@]+)@.*$|\1|')
DB_NAME=$(printf '%s' "$DSN" | sed -E 's|^.*/([^/?]+)(\?.*)?$|\1|')
for v in DB_USER DB_PASS DB_NAME; do
  eval "val=\$$v"
  if [ -z "$val" ] || [ "$val" = "$DSN" ]; then
    echo "could not parse $v out of the DSN" >&2; exit 1
  fi
done
echo "    role=${DB_USER} database=${DB_NAME} password=<not shown>"

if [ "$DRY_RUN" = 1 ]; then
  say "dry run: would create role ${DB_USER} and database ${DB_NAME} on ${PG_NS}/${PG_POD}"
  exit 0
fi

say "creating the role and the database"
# psql does NOT substitute :'pw' inside a dollar-quoted DO block, which is why
# this is built with format() + \gexec rather than the more obvious DO IF NOT
# EXISTS. The CREATEs are skipped when the object exists; the ALTER runs every
# time so a re-run converges on whatever password the secret currently holds.
kubectl -n "$PG_NS" exec -i "$PG_POD" -- env PGPW="$DB_PASS" PGUSR="$DB_USER" PGDB="$DB_NAME" \
  psql -U "$PG_SUPERUSER" -d postgres -v ON_ERROR_STOP=1 <<'SQL'
\set pw `echo "$PGPW"`
\set usr `echo "$PGUSR"`
\set db `echo "$PGDB"`
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'usr', :'pw')
 WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'usr')\gexec
SELECT format('ALTER ROLE %I WITH LOGIN PASSWORD %L', :'usr', :'pw')\gexec
SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'usr')
 WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'db')\gexec
SQL

say "separating the two databases"
kubectl -n "$PG_NS" exec -i "$PG_POD" -- env PGUSR="$DB_USER" PGDB="$DB_NAME" PGSU="$PG_SUPERUSER" \
  psql -U "$PG_SUPERUSER" -d postgres -v ON_ERROR_STOP=1 <<'SQL'
\set usr `echo "$PGUSR"`
\set db `echo "$PGDB"`
\set su `echo "$PGSU"`
-- Take CONNECT off PUBLIC on BOTH databases and hand it back to each owner. See
-- the header: revoking from the role alone leaves the PUBLIC grant in place and
-- the role still connects.
SELECT format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', 'heros')\gexec
SELECT format('GRANT  CONNECT ON DATABASE %I TO %I', 'heros', :'su')\gexec
SELECT format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', :'db')\gexec
SELECT format('GRANT  CONNECT ON DATABASE %I TO %I', :'db', :'usr')\gexec
SQL

say "verifying"
kubectl -n "$PG_NS" exec -i "$PG_POD" -- env PGUSR="$DB_USER" PGDB="$DB_NAME" \
  psql -U "$PG_SUPERUSER" -d postgres -tA <<'SQL'
\set usr `echo "$PGUSR"`
\set db `echo "$PGDB"`
SELECT :'db'   || ' owner: '            || pg_get_userbyid(datdba) FROM pg_database WHERE datname = :'db';
SELECT :'usr'  || ' -> ' || :'db'  || ' CONNECT: ' || has_database_privilege(:'usr', :'db',   'CONNECT');
SELECT :'usr'  || ' -> heros CONNECT: ' || has_database_privilege(:'usr', 'heros', 'CONNECT') || '  (must be false)';
SQL

say "done"
echo "Reminder: a database on this instance is only backed up if it is named in"
echo "the postgres-backup CronJob's loop (heros-agent deploy/k8s/base/postgres.yaml)."
