-- Create "flow_executions" table
CREATE TABLE "flow_executions" ("id" uuid NOT NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "role" character varying NOT NULL, "op_type" integer NOT NULL, "status" character varying NOT NULL DEFAULT 'IN_FLIGHT', "coordinator_index" bigint NOT NULL, "decision_payload" bytea NULL, PRIMARY KEY ("id"));
-- Create index "flowexecution_role_status_update_time" to table: "flow_executions"
CREATE INDEX "flowexecution_role_status_update_time" ON "flow_executions" ("role", "status", "update_time");
