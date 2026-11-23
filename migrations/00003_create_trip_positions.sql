-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE trips ADD COLUMN last_position_at TIMESTAMPTZ;

CREATE TABLE trip_positions (
    id          BIGSERIAL PRIMARY KEY,
    trip_id     UUID NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    latitude    DOUBLE PRECISION NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude   DOUBLE PRECISION NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    recorded_at TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX trip_positions_trip_recorded_idx
    ON trip_positions (trip_id, recorded_at, id);

-- The worker's cutoff and ORDER BY use this same expression. The partial
-- predicate excludes completed trips; CONCURRENTLY avoids blocking inserts.
CREATE INDEX CONCURRENTLY trips_active_stale_at_idx
    ON trips ((COALESCE(last_position_at, started_at)), id)
    WHERE status = 'active';

-- +goose Down
DROP INDEX CONCURRENTLY trips_active_stale_at_idx;
DROP TABLE trip_positions;
ALTER TABLE trips DROP COLUMN last_position_at;
