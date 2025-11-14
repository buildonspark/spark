-- Create table to persist notification events for polling.
CREATE TABLE "event_messages" (
    "id" uuid NOT NULL,
    "create_time" timestamptz NOT NULL,
    "update_time" timestamptz NOT NULL,
    "channel" text NOT NULL,
    "payload" text NOT NULL,
    PRIMARY KEY ("id")
);

CREATE INDEX "event_messages_channel_create_time_id" ON "event_messages" ("channel", "create_time", "id");
