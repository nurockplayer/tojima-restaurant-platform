#!/bin/sh
# The official postgres entrypoint runs this only for a fresh data volume.
set -eu
if [ -z "${KEYCLOAK_DB_PASSWORD:-}" ]; then
    echo 'Keycloak database initialization skipped (no KEYCLOAK_DB_PASSWORD).'
    exit 0
fi
# Pass the password through the environment; psql quoting treats it as data.
# Never enable shell tracing or echo SQL/passwords here.
psql -X -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
\getenv keycloak_password KEYCLOAK_DB_PASSWORD
CREATE ROLE keycloak LOGIN PASSWORD :'keycloak_password';
CREATE DATABASE keycloak OWNER keycloak;
SQL
