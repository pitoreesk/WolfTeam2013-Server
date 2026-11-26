-- Migration 033: Upgrade inventory upgrade column to bigint (64-bit)
ALTER TABLE inventory ALTER COLUMN upgrade TYPE bigint;
