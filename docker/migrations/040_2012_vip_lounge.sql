-- 2012 VIP Lounge (docs/protocol/viplounge_2012.md). Not a priced shop: a
-- spending-reward system. Lounges 0 Cash / 1 Gold / 2 WolfCoin; periods
-- 0 FirstBuyer, 1 Daily, 2 Weekly, 3 Monthly, 4 Quarter, 5 HalfYear, 6 Year,
-- 7 VVip; exactly 5 tiers per period. Spending the lounge currency past a
-- tier threshold within the period window grants one ticket of that tier;
-- one Take consumes one ticket and grants one item (kind 0) or a set (kind 1).
-- The catalog is server data (no client .dat); the seed below is a proposal
-- (docs/data/viplounge_2012.csv). Idempotent: the migrate service re-runs it.
CREATE TABLE IF NOT EXISTS vip_lounges (
    lounge smallint PRIMARY KEY CHECK (lounge BETWEEN 0 AND 2),
    visible boolean NOT NULL DEFAULT true,
    start_date char(8) NOT NULL,
    end_date char(8) NOT NULL
);
CREATE TABLE IF NOT EXISTS vip_lounge_periods (
    lounge smallint NOT NULL REFERENCES vip_lounges(lounge),
    period smallint NOT NULL CHECK (period BETWEEN 0 AND 7),
    enabled boolean NOT NULL DEFAULT true,
    PRIMARY KEY (lounge, period)
);
CREATE TABLE IF NOT EXISTS vip_lounge_tiers (
    lounge smallint NOT NULL,
    period smallint NOT NULL,
    tier smallint NOT NULL CHECK (tier BETWEEN 0 AND 4),
    threshold bigint NOT NULL CHECK (threshold BETWEEN 0 AND 4294967295),
    row_kind smallint NOT NULL DEFAULT 0 CHECK (row_kind IN (0, 1)),
    PRIMARY KEY (lounge, period, tier),
    FOREIGN KEY (lounge, period) REFERENCES vip_lounge_periods(lounge, period)
);
ALTER TABLE vip_shop_items
    ADD COLUMN IF NOT EXISTS period_type smallint NOT NULL DEFAULT 0 CHECK (period_type BETWEEN 0 AND 255),
    ADD COLUMN IF NOT EXISTS stock_limit integer NOT NULL DEFAULT 0 CHECK (stock_limit BETWEEN 0 AND 65535);
DO $$
BEGIN
    -- Sets repeat items across groups, so the key includes group_id.
    IF NOT EXISTS (
        SELECT 1 FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
        WHERE i.indrelid = 'vip_shop_items'::regclass AND i.indisprimary AND a.attname = 'group_id'
    ) THEN
        ALTER TABLE vip_shop_items DROP CONSTRAINT IF EXISTS vip_shop_items_pkey;
        ALTER TABLE vip_shop_items ADD PRIMARY KEY (section, page, slot, group_id, item_id);
    END IF;
END $$;
CREATE TABLE IF NOT EXISTS vip_user_spend (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    lounge smallint NOT NULL CHECK (lounge BETWEEN 0 AND 2),
    period smallint NOT NULL CHECK (period BETWEEN 0 AND 7),
    period_key date NOT NULL,
    amount bigint NOT NULL DEFAULT 0 CHECK (amount >= 0),
    PRIMARY KEY (user_id, lounge, period, period_key)
);
CREATE TABLE IF NOT EXISTS vip_user_tickets (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    lounge smallint NOT NULL,
    period smallint NOT NULL,
    tier smallint NOT NULL CHECK (tier BETWEEN 0 AND 4),
    period_key date NOT NULL,
    granted integer NOT NULL DEFAULT 0,
    used integer NOT NULL DEFAULT 0,
    CHECK (used BETWEEN 0 AND granted),
    PRIMARY KEY (user_id, lounge, period, tier, period_key)
);
CREATE TABLE IF NOT EXISTS vip_item_sales (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    lounge smallint NOT NULL,
    period smallint NOT NULL,
    tier smallint NOT NULL,
    group_id integer NOT NULL,
    item_id integer NOT NULL,
    period_key date NOT NULL,
    sold integer NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, lounge, period, tier, group_id, item_id, period_key)
);

DO $$
BEGIN
IF NOT EXISTS (SELECT 1 FROM vip_lounge_tiers) THEN
    -- 2013-era placeholders are not valid 2012 rows.
    DELETE FROM vip_shop_items;

    INSERT INTO vip_lounges VALUES
        (0, true, '20120901', '20301231'), (1, true, '20120901', '20301231'), (2, false, '20120901', '20301231') -- 2013 TR: WC lounge hidden (does not open)
    ON CONFLICT DO NOTHING;

    -- FirstBuyer (0) is disabled: its grant rule is unknown.
    INSERT INTO vip_lounge_periods
    SELECT l, p.period, p.enabled
    FROM (VALUES (0,false),(1,true),(2,true),(3,true),(4,true),(5,true),(6,true),(7,true)) AS p(period, enabled)
    CROSS JOIN generate_series(0, 2) AS l
    ON CONFLICT DO NOTHING;

    INSERT INTO vip_lounge_tiers
    SELECT b.lounge, p.period, b.tier, b.base * p.mult, b.kind
    FROM (VALUES (0,1),(1,1),(2,3),(3,10),(4,25),(5,50),(6,100),(7,200)) AS p(period, mult)
    CROSS JOIN (VALUES
        (0,0,3000,0),(0,1,10000,0),(0,2,30000,0),(0,3,60000,0),(0,4,100000,1),
        (1,0,50000,0),(1,1,150000,0),(1,2,400000,0),(1,3,800000,0),(1,4,1500000,1),
        (2,0,500,0),(2,1,1500,0),(2,2,5000,0),(2,3,15000,0),(2,4,30000,1)
    ) AS b(lounge, tier, base, kind)
    ON CONFLICT DO NOTHING;

    -- period_type 0 = days (value), 2 = permanent (VVip).
    INSERT INTO vip_shop_items (section, page, slot, group_id, mode, item_id, currency, days, price, enabled, period_type, stock_limit)
    SELECT c.lounge, p.period, t.tier, t.grp, t.kind, t.item, c.cur, p.pval, 0, true, p.ptype, 0
    FROM (VALUES (0,0,30),(1,0,7),(2,0,7),(3,0,30),(4,0,30),(5,0,90),(6,0,90),(7,2,0)) AS p(period, ptype, pval)
    CROSS JOIN (VALUES (0,1),(1,4),(2,2)) AS c(lounge, cur)
    CROSS JOIN (VALUES
        (0,0,0,18004),(0,0,0,16011),(0,0,0,13006),(0,0,0,10082),
        (1,0,0,16012),(1,0,0,16019),(1,0,0,16017),(1,0,0,10081),
        (2,0,0,7502),(2,0,0,7504),(2,0,0,7505),(2,0,0,6016),(2,0,0,6011),(2,0,0,5014),
        (3,0,0,7507),(3,0,0,7509),(3,0,0,7506),(3,0,0,6020),(3,0,0,6021),(3,0,0,4000),
        (4,1,0,7514),(4,1,0,7513),(4,1,1,6021),(4,1,1,6019),(4,1,2,7508),(4,1,2,7510),(4,1,2,7511)
    ) AS t(tier, kind, grp, item)
    ON CONFLICT DO NOTHING;
END IF;
END $$;
