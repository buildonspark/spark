-- Modify "token_allowances" table
ALTER TABLE "token_allowances" ADD COLUMN "flow_execution_id" uuid NULL;
