ALTER TABLE users
    ADD COLUMN IF NOT EXISTS mute_started_at timestamptz,
    ADD COLUMN IF NOT EXISTS mute_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS ban_started_at timestamptz,
    ADD COLUMN IF NOT EXISTS ban_expires_at timestamptz;

UPDATE users
SET mute_started_at = COALESCE(mute_started_at, now())
WHERE muted AND mute_started_at IS NULL;

UPDATE users
SET ban_started_at = COALESCE(ban_started_at, now())
WHERE banned AND ban_started_at IS NULL;

CREATE INDEX IF NOT EXISTS users_active_mute_idx
    ON users (mute_expires_at)
    WHERE muted;

CREATE INDEX IF NOT EXISTS users_active_ban_idx
    ON users (ban_expires_at)
    WHERE banned;
