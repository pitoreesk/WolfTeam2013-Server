ALTER TABLE gifts
    ADD COLUMN IF NOT EXISTS quantity integer NOT NULL DEFAULT 1;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'gifts_quantity_check') THEN
        ALTER TABLE gifts
            ADD CONSTRAINT gifts_quantity_check CHECK (quantity BETWEEN 1 AND 65535);
    END IF;
END $$;
