-- 2013 TR crest (arma) system (docs/protocol/crest_2013.md).
-- The client keeps four independent crest collections, all fed by the server:
--   delegate slots      3 worn emblems        0x134D list 1, 0x134F, roster tail
--   completion percent  per crest, 100 = won  0x134D list 2 percent, 0x145F
--   active challenges   at most 3, 1 / group  0x134D flag, 0x1353, 0x1359
--   group counters      raw event count       0x134D, 0x1699
-- crest_progress (existing) holds the completion percent.

CREATE TABLE IF NOT EXISTS crest_delegate_slots (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slot smallint NOT NULL CHECK (slot BETWEEN 0 AND 2),
    crest_id integer NOT NULL CHECK (crest_id BETWEEN 1 AND 65535),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, slot)
);
CREATE UNIQUE INDEX IF NOT EXISTS crest_delegate_slots_one_slot_per_crest
    ON crest_delegate_slots (user_id, crest_id);

CREATE TABLE IF NOT EXISTS crest_active_challenges (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    crest_id integer NOT NULL CHECK (crest_id BETWEEN 1 AND 65535),
    group_id smallint NOT NULL CHECK (group_id BETWEEN 0 AND 255),
    progress bigint NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 4294967295),
    started_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, crest_id),
    UNIQUE (user_id, group_id)
);

-- The completion percent is a u16 the client compares against exactly 100.
UPDATE crest_progress SET progress = 100 WHERE progress > 100;
