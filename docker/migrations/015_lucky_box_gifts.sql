-- Lucky-box rewards are delivered through the normal Gift Box. A dedicated
-- sender supplies a stable counterparty label in gift lists.
INSERT INTO users (username, password_hash, callsign, level, gp, gold, wc, cash)
VALUES ('lucky_box_system', '!', 'Lucky Box', 61, 0, 0, 0, 0)
ON CONFLICT (username) DO UPDATE SET callsign = EXCLUDED.callsign;
