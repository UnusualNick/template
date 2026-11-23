-- +goose Up
-- The stale-trip predicate filters on this expression, so PostgreSQL needs
-- expression statistics to estimate the cutoff's selectivity accurately.
CREATE STATISTICS trips_active_stale_expr_stats
    ON (COALESCE(last_position_at, started_at)) FROM trips;

-- Populate the statistics immediately for existing trips; do not wait for
-- autovacuum before the worker starts using the index.
ANALYZE trips;

-- +goose Down
DROP STATISTICS trips_active_stale_expr_stats;
