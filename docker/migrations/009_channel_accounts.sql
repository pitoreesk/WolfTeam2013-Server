ALTER TABLE users
    ADD COLUMN IF NOT EXISTS school_id bigint NOT NULL DEFAULT 0
        CHECK (school_id BETWEEN 0 AND 4294967294),
    ADD COLUMN IF NOT EXISTS school_value bigint NOT NULL DEFAULT 0
        CHECK (school_value BETWEEN 0 AND 4294967295),
    ADD COLUMN IF NOT EXISTS wolf_points bigint NOT NULL DEFAULT 0
        CHECK (wolf_points BETWEEN 0 AND 4294967295);

CREATE UNIQUE INDEX IF NOT EXISTS users_callsign_ci_idx ON users (lower(callsign));

CREATE TABLE IF NOT EXISTS recommendations (
    requester_user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status smallint NOT NULL DEFAULT 0 CHECK (status BETWEEN 0 AND 2),
    created_at timestamptz NOT NULL DEFAULT now(),
    answered_at timestamptz,
    PRIMARY KEY (requester_user_id, target_user_id),
    CHECK (requester_user_id <> target_user_id),
    CHECK ((status = 0) = (answered_at IS NULL))
);
CREATE INDEX IF NOT EXISTS recommendations_target_status_idx
    ON recommendations (target_user_id, status, created_at);
