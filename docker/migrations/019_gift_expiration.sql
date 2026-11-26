ALTER TABLE gifts
    ADD COLUMN IF NOT EXISTS expires_at timestamptz;

UPDATE gifts
SET expires_at = created_at + INTERVAL '90 days'
WHERE expires_at IS NULL;

ALTER TABLE gifts
    ALTER COLUMN expires_at SET DEFAULT (now() + INTERVAL '90 days'),
    ALTER COLUMN expires_at SET NOT NULL;

CREATE INDEX IF NOT EXISTS gifts_expiry_idx
    ON gifts (expires_at);
