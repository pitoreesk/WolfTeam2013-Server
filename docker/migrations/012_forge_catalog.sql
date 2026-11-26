-- Configure five Forge rounds with ten choices per category.
WITH forge_rewards(choice_index, reward_item_id) AS (
    VALUES (0,1002),(1,1003),(2,1004),(3,1006),(4,1008),
           (5,1009),(6,1010),(7,1011),(8,1012),(9,1013)
)
INSERT INTO forge_recipes (category, challenge, choice_index, cost_cash, reward_item_id, reward_days)
SELECT category, forge_round, choice_index, 100, reward_item_id,
       (ARRAY[7,14,30,90,0])[forge_round + 1]
FROM generate_series(0, 2) AS category
CROSS JOIN generate_series(0, 4) AS forge_round
CROSS JOIN forge_rewards
-- 2012: migration 039's catalog (marker item 19610) must survive re-runs.
WHERE NOT EXISTS (SELECT 1 FROM forge_recipes WHERE reward_item_id = 19610)
ON CONFLICT (category, challenge, choice_index) DO UPDATE SET
    cost_cash = EXCLUDED.cost_cash,
    reward_item_id = EXCLUDED.reward_item_id,
    reward_days = EXCLUDED.reward_days,
    enabled = true;

-- Disable the obsolete one-choice rows from migration 005.
UPDATE forge_recipes
SET enabled = false
WHERE category BETWEEN 0 AND 2
  AND (challenge NOT BETWEEN 0 AND 4 OR choice_index NOT BETWEEN 0 AND 9)
  AND NOT EXISTS (SELECT 1 FROM forge_recipes WHERE reward_item_id = 19610);
