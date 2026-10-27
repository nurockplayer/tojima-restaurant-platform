#!/bin/sh
# The official postgres entrypoint runs this only for a fresh data volume.
set -eu
if [ -z "${APP_DB_PASSWORD:-}" ]; then
    echo 'Application runtime role initialization skipped (no APP_DB_PASSWORD).'
    exit 0
fi
: "${APP_DB_USER:=tojima_app}"
export APP_DB_USER
# Pass the values through the environment; psql quoting treats them as data.
# Never enable shell tracing or echo SQL/passwords here.
psql -X -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv app_user APP_DB_USER
\getenv app_password APP_DB_PASSWORD
CREATE ROLE :"app_user" LOGIN PASSWORD :'app_password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
SQL
