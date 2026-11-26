ALTER TABLE users DROP CONSTRAINT IF EXISTS users_level_check;
ALTER TABLE users ADD CONSTRAINT users_level_check CHECK (level BETWEEN 1 AND 62);

ALTER TABLE match_rewards DROP CONSTRAINT IF EXISTS match_rewards_level_after_check;
ALTER TABLE match_rewards ADD CONSTRAINT match_rewards_level_after_check CHECK (level_after BETWEEN 1 AND 62);
