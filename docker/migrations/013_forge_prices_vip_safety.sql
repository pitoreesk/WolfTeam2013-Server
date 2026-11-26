-- Apply a nonzero entry fee to configured Forge recipes.
UPDATE forge_recipes
SET cost_cash = 100
WHERE category BETWEEN 0 AND 2
  AND challenge BETWEEN 0 AND 4
  AND choice_index BETWEEN 0 AND 9
  AND NOT EXISTS (SELECT 1 FROM forge_recipes WHERE reward_item_id = 19610);

-- Migration 005 installed an incomplete placeholder row. Keep the table for
-- future catalog data, but disable the row until its metadata is configured.
UPDATE vip_shop_items
SET enabled = false
WHERE section = 0
  AND page = 0
  AND slot = 0
  AND group_id = 0
  AND price = 0
  AND to_regclass('public.vip_lounge_tiers') IS NULL; -- 2012: migration 040 catalog
