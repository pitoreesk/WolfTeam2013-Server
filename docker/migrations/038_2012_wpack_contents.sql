-- 2012 W-Pack contents (docs/protocol/wpack_2012.md). W-Packs are shop items
-- 14901..15000; no client data file defines their contents (setitemdata.dat
-- holds empty placeholders), so the server supplies them via 0x1463 and grants
-- the sub-items on purchase. Proposed contents, editable in this table.
CREATE TABLE IF NOT EXISTS wpack_contents (
    pack_item_id INTEGER  NOT NULL CHECK (pack_item_id BETWEEN 14901 AND 15000),
    position     SMALLINT NOT NULL CHECK (position BETWEEN 1 AND 17),
    item_id      INTEGER  NOT NULL CHECK (item_id BETWEEN 1 AND 65535),
    days         INTEGER  NOT NULL CHECK (days BETWEEN 1 AND 3650),
    PRIMARY KEY (pack_item_id, position),
    UNIQUE (pack_item_id, item_id)
);
-- Seed only an empty table so edited contents survive migrate re-runs.
DO $$
BEGIN
IF NOT EXISTS (SELECT 1 FROM wpack_contents) THEN
INSERT INTO wpack_contents (pack_item_id, position, item_id, days) VALUES
    (14901, 1, 3137, 7), (14901, 2, 3140, 7), (14901, 3, 3200, 7), (14901, 4, 3250, 7), (14901, 5, 3300, 7), (14901, 6, 10017, 7),
    (14902, 1, 3138, 7), (14902, 2, 3141, 7), (14902, 3, 3201, 7), (14902, 4, 3251, 7), (14902, 5, 3253, 7), (14902, 6, 10016, 7),
    (14903, 1, 3139, 7), (14903, 2, 3142, 7), (14903, 3, 3143, 7), (14903, 4, 3202, 7), (14903, 5, 3203, 7), (14903, 6, 3252, 7),
    (14903, 7, 3399, 7), (14904, 1, 6027, 7), (14904, 2, 6028, 7), (14904, 3, 6029, 7), (14904, 4, 6030, 7), (14904, 5, 6031, 7),
    (14904, 6, 6032, 7), (14904, 7, 6033, 7), (14904, 8, 6040, 7), (14905, 1, 7100, 7), (14905, 2, 7101, 7), (14905, 3, 7200, 7),
    (14905, 4, 7507, 7), (14905, 5, 7509, 7), (14905, 6, 7511, 7), (14905, 7, 7513, 7), (14906, 1, 1017, 7), (14906, 2, 2009, 7),
    (14906, 3, 2011, 7), (14906, 4, 2013, 7), (14906, 5, 5016, 7), (14906, 6, 9800, 7), (14907, 1, 10086, 7), (14907, 2, 10087, 7),
    (14907, 3, 10088, 7), (14907, 4, 10089, 7), (14907, 5, 10090, 7), (14907, 6, 10091, 7), (14907, 7, 19602, 7), (14908, 1, 30029, 7),
    (14908, 2, 30030, 7), (14908, 3, 30031, 7), (14908, 4, 30032, 7), (14908, 5, 30033, 7), (14908, 6, 30034, 7), (14909, 1, 31265, 30),
    (14909, 2, 31266, 30), (14909, 3, 31267, 30), (14909, 4, 31268, 30), (14909, 5, 32211, 30), (14909, 6, 32212, 30), (14909, 7, 32213, 30),
    (14909, 8, 32214, 30), (14909, 9, 33133, 30), (14909, 10, 33134, 30), (14909, 11, 33135, 30), (14909, 12, 33436, 30), (14910, 1, 3300, 30),
    (14910, 2, 6030, 30), (14910, 3, 7200, 30), (14910, 4, 2013, 30), (14910, 5, 9800, 30), (14910, 6, 30044, 30), (14910, 7, 30043, 30),
    (14910, 8, 10092, 30)
ON CONFLICT DO NOTHING;
END IF;
END $$;