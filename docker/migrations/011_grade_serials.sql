ALTER TABLE users ALTER COLUMN level SET DEFAULT 61;

-- Grades and current-grade GP are independent authoritative fields. Do not
-- derive the grade here: migrations are replayed by the Compose migration
-- service, and doing so would undo per-grade GP resets and reserved grade 62.
