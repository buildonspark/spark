package ent

//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate --feature intercept,sql/execquery,sql/lock,sql/upsert ./schema
