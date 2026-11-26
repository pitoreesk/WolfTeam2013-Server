-- The client has a dedicated Pride Battle channel category (channel type 2).
-- The default port is deliberately the same value used by compose.yaml. If
-- PRIDE_GAME_PORT is changed, the database row and the compose variable must
-- be changed together; the client receives this endpoint from the broker and
-- does not require a hard-coded port.
INSERT INTO channels (name, port, is_open, enabled, channel_type, f1, f2, f3, f4)
SELECT 'Pride Battle', 40715, true, true, 2, 0, 10000, 0, 0
WHERE NOT EXISTS (
    SELECT 1
    FROM channels
    WHERE lower(name) = lower('Pride Battle')
       OR (channel_type = 2 AND enabled)
);
