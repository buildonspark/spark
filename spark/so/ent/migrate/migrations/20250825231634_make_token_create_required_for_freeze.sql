-- Modify "token_freezes" table
ALTER TABLE "token_freezes" DROP CONSTRAINT "token_freezes_token_creates_token_freeze", ALTER COLUMN "token_create_id" SET NOT NULL, ADD CONSTRAINT "token_freezes_token_creates_token_freeze" FOREIGN KEY ("token_create_id") REFERENCES "token_creates" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
