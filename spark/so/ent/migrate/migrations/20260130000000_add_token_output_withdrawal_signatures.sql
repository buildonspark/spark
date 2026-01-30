-- Modify "token_outputs" table
ALTER TABLE "token_outputs" ADD COLUMN "se_finalization_adaptor_sig" bytea NULL, ADD COLUMN "se_withdrawal_signature" bytea NULL;
