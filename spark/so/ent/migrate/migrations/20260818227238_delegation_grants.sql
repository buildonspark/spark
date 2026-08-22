-- Create "delegation_grants" table
CREATE TABLE "delegation_grants" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "owner_identity_pubkey" bytea NOT NULL,
  "status" character varying NOT NULL DEFAULT 'ACTIVE',
  "network" character varying NOT NULL,
  "expiry_time" timestamptz NOT NULL,
  "scope_transfer" boolean NOT NULL,
  "scope_renew" boolean NOT NULL,
  "scope_claim" boolean NOT NULL,
  "fee_flat_sats" bigint NOT NULL,
  "fee_collector_identity_pubkey" bytea NULL,
  "version" bigint NOT NULL,
  "owner_signature" bytea NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "delegationgrant_owner_identity_pubkey" to table: "delegation_grants"
CREATE INDEX "delegationgrant_owner_identity_pubkey" ON "delegation_grants" ("owner_identity_pubkey");
-- Create "delegation_grant_spenders" table
CREATE TABLE "delegation_grant_spenders" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "spender_identity_pubkey" bytea NOT NULL,
  "status" character varying NOT NULL DEFAULT 'ACTIVE',
  "per_tx_cap_sats" bigint NOT NULL,
  "rolling_limit_sats" bigint NOT NULL,
  "rolling_window_seconds" bigint NOT NULL,
  "per_tx_unlimited" boolean NOT NULL DEFAULT false,
  "rolling_unlimited" boolean NOT NULL DEFAULT false,
  "spent_sats" bigint NOT NULL DEFAULT 0,
  "window_start" timestamptz NULL,
  "version" bigint NOT NULL,
  "owner_signature" bytea NOT NULL,
  "delegation_grant_spenders" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "delegation_grant_spenders_delegation_grants_spenders" FOREIGN KEY ("delegation_grant_spenders") REFERENCES "delegation_grants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "delegationgrantspender_spender_identity_pubkey_status" to table: "delegation_grant_spenders"
CREATE INDEX "delegationgrantspender_spender_identity_pubkey_status" ON "delegation_grant_spenders" ("spender_identity_pubkey", "status");
-- Create index "delegationgrantspender_unique_active_per_grant_spender" to table: "delegation_grant_spenders"
CREATE UNIQUE INDEX "delegationgrantspender_unique_active_per_grant_spender" ON "delegation_grant_spenders" ("spender_identity_pubkey", "delegation_grant_spenders") WHERE ((status)::text = 'ACTIVE'::text);
-- Create "leaf_decompositions" table
CREATE TABLE "leaf_decompositions" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "delegate_signing_pubkey" bytea NOT NULL,
  "status" character varying NOT NULL DEFAULT 'ACTIVE',
  "delegation_grant_leaf_decompositions" uuid NOT NULL,
  "leaf_decomposition_tree_node" uuid NOT NULL,
  "leaf_decomposition_signing_keyshare" uuid NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "leaf_decompositions_delegation_grants_leaf_decompositions" FOREIGN KEY ("delegation_grant_leaf_decompositions") REFERENCES "delegation_grants" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "leaf_decompositions_signing_keyshares_signing_keyshare" FOREIGN KEY ("leaf_decomposition_signing_keyshare") REFERENCES "signing_keyshares" ("id") ON UPDATE NO ACTION ON DELETE SET NULL,
  CONSTRAINT "leaf_decompositions_tree_nodes_tree_node" FOREIGN KEY ("leaf_decomposition_tree_node") REFERENCES "tree_nodes" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "leafdecomposition_delegation_grant_leaf_decompositions" to table: "leaf_decompositions"
CREATE INDEX "leafdecomposition_delegation_grant_leaf_decompositions" ON "leaf_decompositions" ("delegation_grant_leaf_decompositions");
-- Create index "leafdecomposition_unique_active_per_leaf" to table: "leaf_decompositions"
CREATE UNIQUE INDEX "leafdecomposition_unique_active_per_leaf" ON "leaf_decompositions" ("leaf_decomposition_tree_node") WHERE ((status)::text = 'ACTIVE'::text);
