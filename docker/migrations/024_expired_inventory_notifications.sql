CREATE TABLE IF NOT EXISTS expired_inventory_notifications (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id integer NOT NULL CHECK (item_id BETWEEN 1 AND 65535),
    expired_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, item_id)
);

CREATE INDEX IF NOT EXISTS expired_inventory_notifications_user_time_idx
    ON expired_inventory_notifications (user_id, expired_at, item_id);
