-- Create "token_allowance_spends" table
CREATE TABLE "token_allowance_spends" (
  "id" uuid NOT NULL,
  "create_time" timestamptz NOT NULL,
  "update_time" timestamptz NOT NULL,
  "metered_amount" bytea NOT NULL,
  "status" character varying NOT NULL DEFAULT 'RESERVED',
  "token_allowance_id" uuid NOT NULL,
  "token_allowance_spend_token_transaction" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "token_allowance_spends_token_allowances_token_allowance_spend" FOREIGN KEY ("token_allowance_id") REFERENCES "token_allowances" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "token_allowance_spends_token_transactions_token_transaction" FOREIGN KEY ("token_allowance_spend_token_transaction") REFERENCES "token_transactions" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "tokenallowancespend_unique_token_transaction" to table: "token_allowance_spends"
CREATE UNIQUE INDEX "tokenallowancespend_unique_token_transaction" ON "token_allowance_spends" ("token_allowance_spend_token_transaction");
