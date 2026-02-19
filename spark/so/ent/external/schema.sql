-- External table schema for signing_nonces
--
-- ⚠️  DOCUMENTATION ONLY - NOT USED BY ATLAS ⚠️
-- This file documents the actual database structure for manually-managed tables.
-- Atlas uses the Ent schema only (composite schemas require Atlas Pro subscription).
--
-- This table is managed manually outside of Ent/Atlas auto-migrations
-- due to its partitioned structure which requires special handling.
--
-- Migration: scripts/migrate_signing_nonce_to_partitioned.sql
-- Partitioning strategy: RANGE partitioning by id (UUIDv7, 24-hour intervals)
-- Partition management: PurgeAndCreateSigningNoncePartitions() in signingnonce_extension.go

CREATE TABLE IF NOT EXISTS signing_nonces (
    id                UUID                     NOT NULL PRIMARY KEY,
    create_time       TIMESTAMP WITH TIME ZONE NOT NULL,
    update_time       TIMESTAMP WITH TIME ZONE NOT NULL,
    nonce             BYTEA                    NOT NULL,
    nonce_commitment  BYTEA                    NOT NULL,
    retry_fingerprint BYTEA
) PARTITION BY RANGE (id);

-- Index on nonce_commitment for lookups during signing operations
CREATE INDEX IF NOT EXISTS signing_nonces_nonce_commitment
    ON signing_nonces (nonce_commitment);

-- Note: Individual partitions are created/dropped dynamically by application code
-- Partitions are based on UUIDv7 ranges (UUIDv7 encodes timestamp in first 48 bits)
-- Example partition (not created here, just for reference):
-- CREATE TABLE signing_nonces_20260210 PARTITION OF signing_nonces
--     FOR VALUES FROM ('018d7a40-0000-7000-8000-000000000000')  -- Start of 2026-02-10 UTC
--                  TO ('018d7fc0-0000-7000-8000-000000000000');  -- Start of 2026-02-11 UTC
