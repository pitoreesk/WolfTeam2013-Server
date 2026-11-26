ALTER TABLE users
    ADD COLUMN IF NOT EXISTS draws integer NOT NULL DEFAULT 0 CHECK (draws >= 0),
    ADD COLUMN IF NOT EXISTS headshot_kills integer NOT NULL DEFAULT 0 CHECK (headshot_kills >= 0),
    ADD COLUMN IF NOT EXISTS special_weapon_kills integer NOT NULL DEFAULT 0 CHECK (special_weapon_kills >= 0),
    ADD COLUMN IF NOT EXISTS wolf_kills integer NOT NULL DEFAULT 0 CHECK (wolf_kills >= 0),
    ADD COLUMN IF NOT EXISTS play_time_seconds bigint NOT NULL DEFAULT 0
        CHECK (play_time_seconds BETWEEN 0 AND 4294967295);
