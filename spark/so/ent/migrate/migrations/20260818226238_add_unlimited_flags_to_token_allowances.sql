-- Modify "token_allowances" table
ALTER TABLE "token_allowances" ADD COLUMN "per_transaction_unlimited" boolean NULL DEFAULT false, ADD COLUMN "total_unlimited" boolean NULL DEFAULT false;
