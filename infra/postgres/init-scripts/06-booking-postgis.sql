-- 06-booking-postgis.sql — SPEC-W8 A1: enable PostGIS in the booking DB.
-- Runs once on first container start (postgres docker-entrypoint-initdb.d),
-- after 01-booking-schema.sql. Requires the postgis/postgis image (see
-- infra/docker-compose.core.yml). booking-service also bootstraps the
-- extension + geo tables idempotently at boot (store.ensureGeoTables), so
-- this script is belt-and-braces for fresh volumes.
\c booking

CREATE EXTENSION IF NOT EXISTS postgis;
