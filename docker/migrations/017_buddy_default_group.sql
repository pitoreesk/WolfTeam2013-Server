-- Group ID zero represents the built-in Default group. Earlier local
-- schemas incorrectly treated the field as one-based.
BEGIN;

ALTER TABLE buddy_relations
    DROP CONSTRAINT IF EXISTS buddy_relations_group_id_check;

ALTER TABLE buddy_relations
    ALTER COLUMN group_id SET DEFAULT 0;

ALTER TABLE buddy_relations
    ADD CONSTRAINT buddy_relations_group_id_check
    CHECK (group_id BETWEEN 0 AND 65535);

COMMIT;
