-- Create "payment_intents" table
CREATE TABLE "payment_intents" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "payment_intent" character varying NOT NULL, PRIMARY KEY ("id"));
-- Modify "token_transactions" table
ALTER TABLE "token_transactions" ADD COLUMN "token_transaction_payment_intent" uuid NULL, ADD CONSTRAINT "token_transactions_payment_intents_payment_intent" FOREIGN KEY ("token_transaction_payment_intent") REFERENCES "payment_intents" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
-- Modify "transfers" table
ALTER TABLE "transfers" ADD COLUMN "transfer_payment_intent" uuid NULL, ADD CONSTRAINT "transfers_payment_intents_payment_intent" FOREIGN KEY ("transfer_payment_intent") REFERENCES "payment_intents" ("id") ON UPDATE NO ACTION ON DELETE SET NULL;
