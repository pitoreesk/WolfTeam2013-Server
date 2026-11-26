-- Buddy relationships persist each side independently because each player can place the
-- other in a different local group.
CREATE TABLE IF NOT EXISTS buddy_relations (
    owner_user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    buddy_user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id integer NOT NULL DEFAULT 0 CHECK (group_id BETWEEN 0 AND 65535),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_user_id, buddy_user_id),
    CHECK (owner_user_id <> buddy_user_id)
);

CREATE INDEX IF NOT EXISTS buddy_relations_buddy_idx
    ON buddy_relations (buddy_user_id, created_at);
