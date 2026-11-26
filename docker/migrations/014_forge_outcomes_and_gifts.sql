-- Forge success is decided by the server. Later rounds are deliberately less
-- likely to pass; the selected recipe is also the consolation reward when a
-- roll breaks.
ALTER TABLE forge_recipes
    ADD COLUMN IF NOT EXISTS success_percent smallint NOT NULL DEFAULT 100
    CHECK (success_percent BETWEEN 0 AND 100);

UPDATE forge_recipes
SET success_percent = (ARRAY[80,65,50,35,20])[challenge + 1]
WHERE category BETWEEN 0 AND 2
  AND challenge BETWEEN 0 AND 4
  AND choice_index BETWEEN 0 AND 9;

-- Forge rewards are delivered through the normal Gift Box. A distinct system
-- account is required because gifts cannot be sent by a user to themselves.
INSERT INTO users (username, password_hash, callsign, level, gp, gold, wc, cash)
VALUES ('forge_system', '!', 'Item Forge', 61, 0, 0, 0, 0)
ON CONFLICT (username) DO UPDATE SET callsign = EXCLUDED.callsign;
