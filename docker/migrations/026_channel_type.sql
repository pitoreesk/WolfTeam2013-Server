ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS channel_type smallint NOT NULL DEFAULT 0
        CHECK (channel_type BETWEEN 0 AND 4);
