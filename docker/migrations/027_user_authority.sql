ALTER TABLE users
    ADD COLUMN IF NOT EXISTS authority smallint NOT NULL DEFAULT 0
        CHECK (authority BETWEEN 0 AND 255);
