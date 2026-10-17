-- TEST ONLY: proves paired SQL migration smoke mechanics; not a product schema.
CREATE TABLE migration_smoke_probe (
    id INTEGER PRIMARY KEY,
    marker TEXT NOT NULL
);
