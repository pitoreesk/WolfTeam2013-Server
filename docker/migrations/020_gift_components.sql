CREATE TABLE IF NOT EXISTS gift_components (
    gift_id bigint NOT NULL REFERENCES gifts(id) ON DELETE CASCADE,
    position smallint NOT NULL CHECK (position BETWEEN 0 AND 16),
    item_id integer NOT NULL CHECK (item_id BETWEEN 1 AND 65535),
    days smallint NOT NULL CHECK (days BETWEEN 0 AND 3650),
    PRIMARY KEY (gift_id, position),
    UNIQUE (gift_id, item_id)
);
