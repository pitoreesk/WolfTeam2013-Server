CREATE TABLE IF NOT EXISTS character_equipment (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    char_id integer NOT NULL CHECK (char_id > 0),
    slot smallint NOT NULL CHECK (slot BETWEEN 0 AND 15),
    item_unique integer NOT NULL CHECK (item_unique > 0),
    PRIMARY KEY (user_id, char_id, slot)
);

CREATE INDEX IF NOT EXISTS character_equipment_item_idx
    ON character_equipment (user_id, item_unique);
