package ent_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/so/ent/enttest"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	_ "github.com/mattn/go-sqlite3"
)

func TestSigningKeyshare(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&_fk=1")
	defer client.Close()

	ctx := context.Background()

	secretShare := make([]byte, 32)
	rand.Read(secretShare)

	publicShares := make(map[string][]uint8)
	for i := 0; i < 3; i++ {
		identifier := make([]byte, 16)
		rand.Read(identifier)
		publicShare := make([]byte, 32)
		rand.Read(publicShare)
		publicShares[hex.EncodeToString(identifier)] = publicShare
	}

	publicKey := make([]byte, 64)
	rand.Read(publicKey)

	keyshare := client.SigningKeyshare.
		Create().
		SetID(uuid.UUID{}).
		SetStatus(schema.KeyshareStatusAvailable).
		SetMinSigners(5).
		SetSecretShare(secretShare).
		SetPublicShares(publicShares).
		SetPublicKey(publicKey).
		SaveX(ctx)
	if !bytes.Equal(keyshare.SecretShare, secretShare) {
		t.Errorf("secret share not equal")
	}
	if !bytes.Equal(keyshare.PublicKey, publicKey) {
		t.Errorf("public key not equal")
	}
	if len(keyshare.PublicShares) != len(publicShares) {
		t.Errorf("public shares not equal")
	}
	for identifier, expectedPublicShare := range publicShares {
		publicShare, ok := keyshare.PublicShares[identifier]
		if !ok || !bytes.Equal(publicShare, expectedPublicShare) {
			t.Errorf("public shares not equal")
		}
	}
}
