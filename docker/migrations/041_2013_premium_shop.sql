-- 2013 TR Premium Shop (docs/protocol/premiumshop_2013.md).
-- The client unlocks the page when the player's Cash spending reaches a target
-- (CS_IN_PREMIUMSHOP_ITEMLIST_ACK header: reached flag, my used cash, target)
-- and sells shopdata items at server-defined prices. Target, items and prices
-- are design values: the Special Edition characters and two unlisted weapons
-- that the normal shop does not sell.
CREATE TABLE IF NOT EXISTS premium_shop_settings (
    id smallint PRIMARY KEY CHECK (id = 1),
    enabled boolean NOT NULL DEFAULT true,
    target_cash integer NOT NULL DEFAULT 30000 CHECK (target_cash >= 0)
);

CREATE TABLE IF NOT EXISTS premium_shop_items (
    item_id integer NOT NULL CHECK (item_id BETWEEN 1 AND 65535),
    days smallint NOT NULL CHECK (days IN (1, 7, 30, 90, 180, 0)),
    price bigint NOT NULL CHECK (price BETWEEN 1 AND 4294967295),
    wc_reward bigint NOT NULL DEFAULT 0 CHECK (wc_reward BETWEEN 0 AND 4294967295),
    sort_order integer NOT NULL DEFAULT 0,
    enabled boolean NOT NULL DEFAULT true,
    PRIMARY KEY (item_id, days)
);

INSERT INTO premium_shop_settings (id, enabled, target_cash) VALUES (1, true, 30000)
ON CONFLICT (id) DO NOTHING;

DO $$
BEGIN
IF NOT EXISTS (SELECT 1 FROM premium_shop_items) THEN
    INSERT INTO premium_shop_items (item_id, days, price, sort_order) VALUES
        (30069, 30, 15000, 1), (30070, 30, 15000, 2), (30071, 30, 15000, 3), (30072, 30, 15000, 4),
        (30048, 30, 15000, 5), (30049, 30, 15000, 6), (30050, 30, 15000, 7), (30051, 30, 15000, 8),
        (7112, 7, 20000, 9), (7112, 30, 80000, 9),
        (7113, 7, 25000, 10), (7113, 30, 100000, 10);
END IF;
END $$;
