-- The 2012 client has no characters 30069/30071 (they are absent from the
-- 2012 ShopData.dat); the seed admin account from the 2013 schema selected
-- them. Replace them with the default pair and drop the unusable items.
-- Idempotent: the migrate service re-runs every file on start.
UPDATE character_loadout SET red_char_id = 30006, updated_at = now()
WHERE red_char_id IN (30069, 30071);
UPDATE character_loadout SET blue_char_id = 30005, updated_at = now()
WHERE blue_char_id IN (30069, 30071);
DELETE FROM character_equipment WHERE char_id IN (30069, 30071);
DELETE FROM char_slots WHERE char_id IN (30069, 30071);
DELETE FROM inventory WHERE item_id IN (30069, 30071);
