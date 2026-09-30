#!/usr/bin/env bash
# Creates the itakeit user and database on an existing Postgres server, for use
# instead of the bundled postgres.yaml. The bot creates its own tables on start,
# so this is all the setup it needs.
#
#   ./setup-db.sh postgres://admin@db.example.com:5432/postgres
#
# The argument is an admin connection (a superuser, or a role with CREATEROLE and
# CREATEDB); without it psql uses the PG* environment variables. Safe to re-run:
# an existing user keeps its password unless ITAKEIT_DB_PASSWORD is set.
#
# ITAKEIT_DB_USER, ITAKEIT_DB_NAME  default itakeit
# ITAKEIT_DB_PASSWORD               default a new random hex password (URL-safe)
set -euo pipefail

user=${ITAKEIT_DB_USER:-itakeit}
db=${ITAKEIT_DB_NAME:-itakeit}
reset=${ITAKEIT_DB_PASSWORD:+1}
pw=${ITAKEIT_DB_PASSWORD:-$(openssl rand -hex 24)}

run() { psql "${1:-}" -v ON_ERROR_STOP=1 -qAt -v user="$user" -v db="$db" -v pw="$pw"; }
existed=$(run "$@" <<<"SELECT count(*) FROM pg_roles WHERE rolname = :'user'")

# \gexec runs each generated statement; the WHERE clauses skip what exists.
# Owning the database also gives the user its public schema (Postgres 15+ no
# longer lets other users create tables there).
run "$@" <<SQL
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', :'user', :'pw') WHERE $existed = 0 \\gexec
SELECT format('ALTER ROLE %I PASSWORD %L', :'user', :'pw') WHERE $existed = 1 AND '${reset:-0}' = '1' \\gexec
SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'user')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'db') \\gexec
SQL

if [[ $existed == 0 || -n $reset ]]; then
  echo "Store the password for the deployment:"
  echo "  kubectl -n itakeit create secret generic itakeit-postgres --from-literal=password=$pw --dry-run=client -o yaml | kubectl apply -f -"
else
  echo "User $user already existed and kept its password."
fi
echo "Then point ITAKEIT_DATABASE_URL in deployment.yaml at this server (user $user, database $db) and remove postgres.yaml from kustomization.yaml."
