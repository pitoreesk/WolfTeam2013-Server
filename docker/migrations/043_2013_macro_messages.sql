-- 2013 TR chat macros (CS_CH_SETMACROMESSAGE_REQ 0x132E / GETMACROMESSAGE
-- 0x132C): five slots of {u8 flag, UTF-16 text} kept per account.
CREATE TABLE IF NOT EXISTS user_macros (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slot smallint NOT NULL CHECK (slot BETWEEN 0 AND 4),
    flag smallint NOT NULL DEFAULT 0 CHECK (flag BETWEEN 0 AND 255),
    text varchar(127) NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, slot)
);
