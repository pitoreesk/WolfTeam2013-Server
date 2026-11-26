-- Default 2012 Battle Room (tutorial) chapter rewards: one 7-day Gold-shop
-- item per chapter (Human 1-2 = track 1, Wolf 1-3 = track 2). The original
-- reward data is server-side and unknown; these are placeholders.
-- Inserted only while the table is empty, so edited rewards are kept when
-- the migrate service re-runs every file.
INSERT INTO tutorial_rewards (track, level, item_id, days)
SELECT track, level, item_id, days FROM (VALUES
    (1, 1, 1002, 7),
    (1, 2, 9002, 7),
    (2, 1, 13001, 7),
    (2, 2, 16014, 7),
    (2, 3, 12022, 7)
) AS defaults(track, level, item_id, days)
WHERE NOT EXISTS (SELECT 1 FROM tutorial_rewards);
