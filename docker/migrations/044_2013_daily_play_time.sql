-- 2013 TR P.T. (play time) Gold bonus: PTBonusGold.dat pays at 30-minute
-- steps of the day's play time (up to 24 h). USERINFO/GAMEPOINT/ENDGAME
-- carry the day's play time for the client gauge (sub_34006200).
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_play_seconds integer NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS daily_play_date date;
ALTER TABLE users ADD COLUMN IF NOT EXISTS pt_bonus_paid smallint NOT NULL DEFAULT 0;
