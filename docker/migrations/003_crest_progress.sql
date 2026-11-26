CREATE TABLE IF NOT EXISTS crest_progress (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    crest_id integer NOT NULL CHECK (crest_id BETWEEN 0 AND 65535),
    progress bigint NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 4294967295),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, crest_id)
);
