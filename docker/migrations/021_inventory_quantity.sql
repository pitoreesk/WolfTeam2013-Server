ALTER TABLE inventory
    ADD COLUMN IF NOT EXISTS quantity integer NOT NULL DEFAULT 1;

ALTER TABLE inventory
    ALTER COLUMN paid_price TYPE bigint;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'inventory_quantity_check') THEN
        ALTER TABLE inventory ADD CONSTRAINT inventory_quantity_check CHECK (quantity > 0);
    END IF;
END $$;

-- Permanent inventory rows historically represented quantity by repetition.
-- Compact compatible rows while preserving their total quantity and refundable
-- acquisition price. Currency remains part of the key so refunds stay correct.
WITH grouped AS (
    SELECT min(id) AS keep_id,
           array_agg(id) AS row_ids,
           sum(quantity)::integer AS total_quantity,
           sum(paid_price)::bigint AS total_paid_price
    FROM inventory
    WHERE expiry IN ('permanent', '20991231235959')
    GROUP BY user_id, item_id, type, upgrade, paid_currency
    HAVING count(*) > 1
), updated AS (
    UPDATE inventory AS i
    SET quantity = g.total_quantity,
        paid_price = g.total_paid_price,
        expiry = '20991231235959'
    FROM grouped AS g
    WHERE i.id = g.keep_id
    RETURNING i.id
)
DELETE FROM inventory AS i
USING grouped AS g
WHERE i.id = ANY(g.row_ids) AND i.id <> g.keep_id;
