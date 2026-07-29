package brontide

// The init() guard in accessors.go already panics at process start if the brontide field layout ever drifts, but these
// tests pin the runtime behavior so a layout drift surfaces as a test failure too, and they document what the helper
// returns at each stage of the handshake.

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/brontide"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandshakeDeadline(t *testing.T) {
	t.Run("nil context uses fallback", func(t *testing.T) {
		deadline := handshakeDeadline(nil, time.Second)
		assert.WithinDuration(t, time.Now().Add(time.Second), deadline, 50*time.Millisecond)
	})

	t.Run("context without deadline uses fallback", func(t *testing.T) {
		deadline := handshakeDeadline(t.Context(), time.Second)
		assert.WithinDuration(t, time.Now().Add(time.Second), deadline, 50*time.Millisecond)
	})

	t.Run("context deadline beyond fallback uses fallback", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(time.Hour))
		defer cancel()
		deadline := handshakeDeadline(ctx, time.Second)
		assert.WithinDuration(t, time.Now().Add(time.Second), deadline, 50*time.Millisecond)
	})

	t.Run("earlier context deadline wins over fallback", func(t *testing.T) {
		earlier := time.Now().Add(100 * time.Millisecond)
		ctx, cancel := context.WithDeadline(t.Context(), earlier)
		t.Cleanup(cancel)

		deadline := handshakeDeadline(ctx, time.Hour)
		assert.Equal(t, earlier, deadline)
	})
}

func TestMachineRemoteStatic(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	initiator := keys.MustGeneratePrivateKeyFromRand(rng)
	responder := keys.MustGeneratePrivateKeyFromRand(rng)

	t.Run("initiator knows responder pubkey from construction", func(t *testing.T) {
		m := brontide.NewBrontideMachine(true, newLocalECDH(initiator), responder.Public().ToBTCEC())
		remoteStatic, ok := machineRemoteStatic(m)
		require.True(t, ok)
		assert.Equal(t, responder.Public(), remoteStatic)
	})

	t.Run("responder has no remote static before handshake", func(t *testing.T) {
		m := brontide.NewBrontideMachine(false, newLocalECDH(responder), nil)
		_, ok := machineRemoteStatic(m)
		assert.False(t, ok)
	})

	t.Run("responder learns initiator pubkey after RecvActThree", func(t *testing.T) {
		initiatorM := brontide.NewBrontideMachine(true, newLocalECDH(initiator), responder.Public().ToBTCEC())
		responderM := brontide.NewBrontideMachine(false, newLocalECDH(responder), nil)

		actOne, err := initiatorM.GenActOne()
		require.NoError(t, err)
		require.NoError(t, responderM.RecvActOne(actOne))

		actTwo, err := responderM.GenActTwo()
		require.NoError(t, err)
		require.NoError(t, initiatorM.RecvActTwo(actTwo))

		actThree, err := initiatorM.GenActThree()
		require.NoError(t, err)
		require.NoError(t, responderM.RecvActThree(actThree))

		remoteStatic, ok := machineRemoteStatic(responderM)
		require.True(t, ok)
		assert.Equal(t, initiator.Public(), remoteStatic)
	})
}

func TestLocalECDHPubKey(t *testing.T) {
	rng := rand.NewChaCha8([32]byte{})
	priv := keys.MustGeneratePrivateKeyFromRand(rng)
	ecdh := newLocalECDH(priv)
	assert.Equal(t, priv.Public().Serialize(), ecdh.PubKey().SerializeCompressed())
}
