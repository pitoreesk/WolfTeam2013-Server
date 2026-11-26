CREATE TABLE IF NOT EXISTS tutorial_progress (
    user_id integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    human_level smallint NOT NULL DEFAULT 0 CHECK (human_level BETWEEN 0 AND 255),
    wolf_level smallint NOT NULL DEFAULT 0 CHECK (wolf_level BETWEEN 0 AND 255),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tutorial_rewards (
    track smallint NOT NULL CHECK (track IN (1, 2)),
    level smallint NOT NULL CHECK (level BETWEEN 1 AND 255),
    item_id integer NOT NULL CHECK (item_id BETWEEN 1 AND 29999),
    days smallint NOT NULL DEFAULT 0 CHECK (days BETWEEN 0 AND 3650),
    PRIMARY KEY (track, level, item_id)
);

CREATE TABLE IF NOT EXISTS tutorial_claims (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    track smallint NOT NULL CHECK (track IN (1, 2)),
    level smallint NOT NULL CHECK (level BETWEEN 1 AND 255),
    claimed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, track, level)
);
