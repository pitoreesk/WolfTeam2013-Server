-- Pride ranking state (2013): the best rank a Pride has reached (BATTLERESULT
-- peak rank) and daily rank snapshots (RECENTLYRANK history).
ALTER TABLE prides ADD COLUMN IF NOT EXISTS best_rank integer NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS pride_rank_snapshots (
    pride_id bigint  NOT NULL REFERENCES prides(id) ON DELETE CASCADE,
    day      date    NOT NULL,
    rank     integer NOT NULL,
    points   integer NOT NULL,
    PRIMARY KEY (pride_id, day)
);
