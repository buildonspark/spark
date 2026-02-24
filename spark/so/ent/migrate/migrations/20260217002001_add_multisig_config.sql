-- Create "multisig_configs" table
CREATE TABLE "multisig_configs" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "multisig_identifier" bytea NOT NULL, "num_signers_threshold" bigint NOT NULL, "num_signers_total" bigint NOT NULL, PRIMARY KEY ("id"));
-- Create index "multisig_configs_multisig_identifier_key" to table: "multisig_configs"
CREATE UNIQUE INDEX "multisig_configs_multisig_identifier_key" ON "multisig_configs" ("multisig_identifier");
-- Create "multisig_members" table
CREATE TABLE "multisig_members" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "public_key" bytea NOT NULL, "multisig_config_members" uuid NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "multisig_members_multisig_configs_members" FOREIGN KEY ("multisig_config_members") REFERENCES "multisig_configs" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create index "multisigmember_multisig_config_members" to table: "multisig_members"
CREATE INDEX "multisigmember_multisig_config_members" ON "multisig_members" ("multisig_config_members");
-- Create index "multisigmember_public_key_multisig_config_members" to table: "multisig_members"
CREATE UNIQUE INDEX "multisigmember_public_key_multisig_config_members" ON "multisig_members" ("public_key", "multisig_config_members");
