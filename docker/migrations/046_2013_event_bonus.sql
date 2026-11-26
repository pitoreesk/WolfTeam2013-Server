-- 1+1 Etkinlik ("Ekstra 1 Etkinliği"): buying the listed shop option of an
-- item also sends a bonus item to the buyer's Gift Box. The client receives
-- the list through 0x1459 CS_IN_EVENT_REWARD_LIST_ACK (sub_34177130) and shows
-- the bonus popup (sub_3427D180) only for the matching option.
--
-- required_unit / bonus_unit: 0 = days, 1 = count (string 8213 "%s Gün",
-- 8214 "%d T", sub_3407D0C0).
CREATE TABLE IF NOT EXISTS event_bonus_items (
    item_id        integer  PRIMARY KEY CHECK (item_id BETWEEN 1 AND 65535),
    required_unit  smallint NOT NULL DEFAULT 1 CHECK (required_unit IN (0, 1)),
    required_value integer  NOT NULL CHECK (required_value BETWEEN 1 AND 65535),
    bonus_item_id  integer  NOT NULL CHECK (bonus_item_id BETWEEN 1 AND 65535),
    bonus_unit     smallint NOT NULL DEFAULT 1 CHECK (bonus_unit IN (0, 1)),
    bonus_amount   integer  NOT NULL CHECK (bonus_amount BETWEEN 1 AND 65535),
    enabled        boolean  NOT NULL DEFAULT true
);

-- Every box sold in the shop (17001, 17003-17007, 17009-17019): buy "1 T"
-- and receive 30 more of the same box (user request 2026-09-25).
INSERT INTO event_bonus_items (item_id, required_unit, required_value, bonus_item_id, bonus_unit, bonus_amount)
SELECT id, 1, 1, id, 1, 30
FROM unnest(ARRAY[17001, 17003, 17004, 17005, 17006, 17007, 17009, 17010, 17011,
                  17012, 17013, 17014, 17015, 17016, 17017, 17018, 17019]) AS box(id)
ON CONFLICT (item_id) DO NOTHING;

-- Gift Box sender label for the bonus gifts.
INSERT INTO users (username, password_hash, callsign, level, gp, gold, wc, cash)
VALUES ('event_system', '!', '1+1 Etkinlik', 61, 0, 0, 0, 0)
ON CONFLICT (username) DO UPDATE SET callsign = EXCLUDED.callsign;
