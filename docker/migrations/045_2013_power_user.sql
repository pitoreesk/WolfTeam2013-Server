-- 2013 TR Power User (10027): chat color and name prefix (0x1328) survive
-- relogs, and each Power User grant shields the next 100 deaths from the
-- kill/death record ("100 Ölüm'den Kurtulma", item description).
ALTER TABLE users ADD COLUMN IF NOT EXISTS power_user_color bigint NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS power_user_label varchar(32) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS death_shield integer NOT NULL DEFAULT 0;
