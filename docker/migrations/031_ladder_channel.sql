-- The client has a dedicated Pride Ladder channel category (channel type 3).
-- The default port is deliberately the same value used by compose.yaml. If
-- LADDER_GAME_PORT is changed, the database row and the compose variable must
-- be changed together; the client receives this endpoint from the broker and
-- does not require a hard-coded port.
INSERT INTO channels (name, port, is_open, enabled, channel_type, f1, f2, f3, f4)
SELECT 'Ladder', 40711, true, true, 3, 0, 10000, 0, 0
WHERE NOT EXISTS (
    SELECT 1
    FROM channels
    WHERE lower(name) = lower('Ladder')
       OR (channel_type = 3 AND enabled)
);
