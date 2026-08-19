package tokens

import (
	"bytes"
	"context"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/protohash"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/consensus"
	"github.com/lightsparkdev/spark/so/db"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/tokencreate"
	"github.com/lightsparkdev/spark/so/ent/tokentransaction"
	"github.com/lightsparkdev/spark/so/entfixtures"
	"github.com/lightsparkdev/spark/so/knobs"
	"github.com/lightsparkdev/spark/so/protoconverter"
	"github.com/lightsparkdev/spark/so/utils"
	sparktesting "github.com/lightsparkdev/spark/testing"
)

type tokenTransactionFlowTestSetup struct {
	t        *testing.T
	config   *so.Config
	ctx      context.Context
	client   *ent.Client
	fixtures *entfixtures.Fixtures
	handler  *TokenTransactionFlowHandler

	coordinatorID string
	// otherOperatorKey is a second configured operator whose private key the
	// test holds, so commit payloads can carry real verifiable signatures.
	otherOperatorKey keys.Private
	otherOperatorID  string
}

func setUpTokenTransactionFlowTest(t *testing.T) *tokenTransactionFlowTestSetup {
	t.Helper()

	config := sparktesting.TestConfig(t)
	ctx, _ := db.ConnectToTestPostgres(t)
	ctx = knobs.InjectKnobsService(ctx, knobs.NewFixedKnobs(map[string]float64{}))
	dbClient, err := ent.GetDbFromContext(ctx)
	require.NoError(t, err)
	fixtures := entfixtures.New(t, ctx, dbClient)
	fixtures.CreateKeyshareWithEntityDkgKey()

	coordinatorID := config.Identifier
	coordinatorPubKey := config.SigningOperatorMap[coordinatorID].IdentityPublicKey
	otherOperatorKey := keys.GeneratePrivateKey()
	otherOperatorID := so.IndexToIdentifier(1)
	if otherOperatorID == coordinatorID {
		otherOperatorID = so.IndexToIdentifier(2)
	}
	config.SigningOperatorMap = map[string]*so.SigningOperator{
		coordinatorID: {
			Identifier:        coordinatorID,
			IdentityPublicKey: coordinatorPubKey,
		},
		otherOperatorID: {
			Identifier:        otherOperatorID,
			IdentityPublicKey: otherOperatorKey.Public(),
		},
	}
	config.Lrc20Configs = map[string]so.Lrc20Config{
		strings.ToLower(btcnetwork.Regtest.String()): {
			WithdrawBondSats:              1000,
			WithdrawRelativeBlockLocktime: 100,
		},
	}

	return &tokenTransactionFlowTestSetup{
		t:                t,
		config:           config,
		ctx:              ctx,
		client:           dbClient,
		fixtures:         fixtures,
		handler:          NewTokenTransactionFlowHandler(config),
		coordinatorID:    coordinatorID,
		otherOperatorKey: otherOperatorKey,
		otherOperatorID:  otherOperatorID,
	}
}

func (s *tokenTransactionFlowTestSetup) sortedOperatorKeys() [][]byte {
	opKeys := make([][]byte, 0, len(s.config.SigningOperatorMap))
	for _, op := range s.config.GetSigningOperatorList() {
		opKeys = append(opKeys, op.GetPublicKey())
	}
	slices.SortFunc(opKeys, bytes.Compare)
	return opKeys
}

func (s *tokenTransactionFlowTestSetup) buildCreatePartial(issuerKey keys.Private, tokenName string) *tokenpb.PartialTokenTransaction {
	return &tokenpb.PartialTokenTransaction{
		Version: 3,
		TokenTransactionMetadata: &tokenpb.TokenTransactionMetadata{
			SparkOperatorIdentityPublicKeys: s.sortedOperatorKeys(),
			Network:                         sparkpb.Network_REGTEST,
			ClientCreatedTimestamp:          timestamppb.New(utils.ToMicrosecondPrecision(time.Now().UTC())),
			ValidityDurationSeconds:         300,
		},
		TokenInputs: &tokenpb.PartialTokenTransaction_CreateInput{
			CreateInput: &tokenpb.TokenCreateInput{
				IssuerPublicKey: issuerKey.Public().Serialize(),
				TokenName:       tokenName,
				TokenTicker:     "TST",
				Decimals:        8,
				MaxSupply:       make([]byte, 16),
				IsFreezable:     false,
			},
		},
	}
}

// buildCreatePrepareOp constructs a valid consensus prepare op for a create:
// the client-signed partial converted to the legacy final wire shape with the
// creation entity public key filled from this SO's entity DKG key, exactly as
// the coordinator entrypoint constructs it.
func (s *tokenTransactionFlowTestSetup) buildCreatePrepareOp(issuerKey keys.Private, tokenName string) (*pbinternal.TokenTransactionPrepareRequest, []byte) {
	partial := s.buildCreatePartial(issuerKey, tokenName)
	partialHash, err := protohash.Hash(partial)
	require.NoError(s.t, err)
	sig, err := schnorr.Sign(issuerKey.ToBTCEC(), partialHash)
	require.NoError(s.t, err)

	legacyFinal, err := protoconverter.ConvertPartialToV2TxShape(partial)
	require.NoError(s.t, err)
	dkgPubKey, err := ent.GetEntityDkgKeyPublicKey(s.ctx, s.client)
	require.NoError(s.t, err)
	legacyFinal.GetCreateInput().CreationEntityPublicKey = dkgPubKey.Serialize()

	finalHash, err := utils.HashTokenTransaction(legacyFinal, false)
	require.NoError(s.t, err)

	return &pbinternal.TokenTransactionPrepareRequest{
		FinalTokenTransaction: legacyFinal,
		TokenTransactionSignatures: []*tokenpb.SignatureWithIndex{
			{InputIndex: 0, Signature: sig.Serialize()},
		},
	}, finalHash
}

// prepare runs the flow handler's Prepare and returns this SO's signature.
func (s *tokenTransactionFlowTestSetup) prepare(ctx context.Context, op *pbinternal.TokenTransactionPrepareRequest) []byte {
	result, err := s.handler.Prepare(ctx, op)
	require.NoError(s.t, err)
	resp, ok := result.(*pbinternal.TokenTransactionPrepareResponse)
	require.True(s.t, ok)
	require.NotEmpty(s.t, resp.GetSparkOperatorSignature())
	return resp.GetSparkOperatorSignature()
}

// buildCommitRequest builds a commit payload with the coordinator's real
// signature (from Prepare) plus the second operator's signature over the
// final hash, mirroring what BuildCommitPayload gossips out.
func (s *tokenTransactionFlowTestSetup) buildCommitRequest(finalHash, coordinatorSig []byte) *pbinternal.TokenTransactionCommitRequest {
	otherSig := ecdsa.Sign(s.otherOperatorKey.ToBTCEC(), finalHash).Serialize()
	return &pbinternal.TokenTransactionCommitRequest{
		FinalTokenTransactionHash: finalHash,
		OperatorTransactionSignatures: []*pbinternal.TokenTransactionOperatorSignature{
			{
				OperatorIdentityPublicKey: s.config.SigningOperatorMap[s.coordinatorID].IdentityPublicKey.Serialize(),
				Signature:                 coordinatorSig,
			},
			{
				OperatorIdentityPublicKey: s.otherOperatorKey.Public().Serialize(),
				Signature:                 otherSig,
			},
		},
	}
}

func (s *tokenTransactionFlowTestSetup) fetchTransaction(finalHash []byte) *ent.TokenTransaction {
	tx, err := s.client.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalHash)).
		WithCreate().
		WithPeerSignatures().
		Only(s.ctx)
	require.NoError(s.t, err)
	return tx
}

func TestTokenTransactionFlowPrepare_CreateSuccess(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")

	signature := s.prepare(s.ctx, op)

	require.NoError(t, common.VerifyECDSASignature(s.config.IdentityPublicKey(), signature, finalHash))
	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusSigned, tx.Status)
	require.NotNil(t, tx.Edges.Create)
	assert.NotEmpty(t, tx.Edges.Create.TokenIdentifier)
	// No ctx coordinator identity means the coordinator's own self-Prepare:
	// the recorded coordinator is this SO.
	assert.Equal(t, s.config.IdentityPublicKey(), tx.CoordinatorPublicKey)
}

func TestTokenTransactionFlowPrepare_RecordsEngineCoordinatorIdentity(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")

	participantCtx := consensus.WithCoordinatorIdentity(s.ctx, s.otherOperatorKey.Public())
	s.prepare(participantCtx, op)

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, s.otherOperatorKey.Public(), tx.CoordinatorPublicKey)
}

func TestTokenTransactionFlowPrepare_RefusesPreexistingTransaction(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")

	s.prepare(s.ctx, op)

	// A second prepare for the same final hash (a retried attempt aliasing
	// onto the first attempt's row) must fail closed rather than adopt the
	// row: the first attempt's pending rollback deletes by final hash, so an
	// adopted row could be deleted out from under the newer attempt.
	_, err := s.handler.Prepare(s.ctx, op)
	require.ErrorContains(t, err, "refusing to adopt")

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusSigned, tx.Status)
	count, err := s.client.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalHash)).
		Count(s.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestTokenTransactionFlowPrepare_RejectsNonCreate(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	_, tokenCreate := s.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)

	issuerKey := s.fixtures.GeneratePrivateKey()
	op, _ := s.buildCreatePrepareOp(issuerKey, "Test Token")
	op.FinalTokenTransaction.TokenInputs = &tokenpb.TokenTransaction_MintInput{
		MintInput: &tokenpb.TokenMintInput{
			IssuerPublicKey: issuerKey.Public().Serialize(),
			TokenIdentifier: tokenCreate.TokenIdentifier,
		},
	}

	_, err := s.handler.Prepare(s.ctx, op)
	require.ErrorContains(t, err, "supports only")
}

func TestTokenTransactionFlowPrepare_RejectsWrongOpType(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	_, err := s.handler.Prepare(s.ctx, &pbinternal.TokenTransactionCommitRequest{})
	require.ErrorContains(t, err, "unexpected operation type")
}

func TestTokenTransactionFlowPrepare_RejectsDuplicateTokenIdentifier(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()

	firstOp, firstHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	s.prepare(s.ctx, firstOp)

	// Same metadata (same deterministic token identifier) but a fresh client
	// timestamp, so the transaction hashes differ and the duplicate is caught
	// by the identifier check rather than the same-hash refusal.
	secondOp, secondHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	require.NotEqual(t, firstHash, secondHash, "test requires distinct transaction hashes")
	_, err := s.handler.Prepare(s.ctx, secondOp)
	require.Error(t, err)
	require.ErrorContains(t, err, "already created")
}

func TestTokenTransactionFlowCommit_Success(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	coordinatorSig := s.prepare(s.ctx, op)

	commitReq := s.buildCommitRequest(finalHash, coordinatorSig)
	require.NoError(t, s.handler.Commit(s.ctx, commitReq))

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusFinalized, tx.Status)
	// Only the other operator's signature lands in the peer signature table;
	// this SO's own signature is never written there.
	require.Len(t, tx.Edges.PeerSignatures, 1)
	assert.Equal(t, s.otherOperatorKey.Public(), tx.Edges.PeerSignatures[0].OperatorIdentityPublicKey)
}

func TestTokenTransactionFlowCommit_IdempotentRedelivery(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	coordinatorSig := s.prepare(s.ctx, op)

	commitReq := s.buildCommitRequest(finalHash, coordinatorSig)
	require.NoError(t, s.handler.Commit(s.ctx, commitReq))
	require.NoError(t, s.handler.Commit(s.ctx, commitReq))

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusFinalized, tx.Status)
	assert.Len(t, tx.Edges.PeerSignatures, 1)
}

func TestTokenTransactionFlowCommit_MissingTransactionIsError(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	fakeHash := s.fixtures.RandomBytes(32)
	commitReq := s.buildCommitRequest(fakeHash, ecdsa.Sign(s.otherOperatorKey.ToBTCEC(), fakeHash).Serialize())
	require.ErrorContains(t, s.handler.Commit(s.ctx, commitReq), "not found")
}

func TestTokenTransactionFlowCommit_RejectsUnknownOperatorKey(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	coordinatorSig := s.prepare(s.ctx, op)

	strangerKey := keys.GeneratePrivateKey()
	commitReq := &pbinternal.TokenTransactionCommitRequest{
		FinalTokenTransactionHash: finalHash,
		OperatorTransactionSignatures: []*pbinternal.TokenTransactionOperatorSignature{
			{
				OperatorIdentityPublicKey: s.config.IdentityPublicKey().Serialize(),
				Signature:                 coordinatorSig,
			},
			{
				OperatorIdentityPublicKey: strangerKey.Public().Serialize(),
				Signature:                 ecdsa.Sign(strangerKey.ToBTCEC(), finalHash).Serialize(),
			},
		},
	}
	require.ErrorContains(t, s.handler.Commit(s.ctx, commitReq), "not a configured signing operator")
}

func TestTokenTransactionFlowCommit_RejectsDuplicateOperatorSignature(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	coordinatorSig := s.prepare(s.ctx, op)

	otherSig := ecdsa.Sign(s.otherOperatorKey.ToBTCEC(), finalHash).Serialize()
	commitReq := &pbinternal.TokenTransactionCommitRequest{
		FinalTokenTransactionHash: finalHash,
		OperatorTransactionSignatures: []*pbinternal.TokenTransactionOperatorSignature{
			{
				OperatorIdentityPublicKey: s.config.IdentityPublicKey().Serialize(),
				Signature:                 coordinatorSig,
			},
			{
				OperatorIdentityPublicKey: s.otherOperatorKey.Public().Serialize(),
				Signature:                 otherSig,
			},
			{
				OperatorIdentityPublicKey: s.otherOperatorKey.Public().Serialize(),
				Signature:                 otherSig,
			},
		},
	}
	require.ErrorContains(t, s.handler.Commit(s.ctx, commitReq), "duplicate operator signature")

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusSigned, tx.Status)
}

func TestTokenTransactionFlowCommit_RejectsNonCreateTransaction(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	_, tokenCreate := s.fixtures.CreateTokenCreateWithIssuer(btcnetwork.Regtest, nil, nil)
	mintTx, _ := s.fixtures.CreateMintTransaction(
		tokenCreate,
		entfixtures.OutputSpecsWithOwner(s.otherOperatorKey.Public(), big.NewInt(5)),
		st.TokenTransactionStatusSigned,
	)

	coordinatorSig := ecdsa.Sign(s.config.IdentityPrivateKey.ToBTCEC(), mintTx.FinalizedTokenTransactionHash).Serialize()
	commitReq := s.buildCommitRequest(mintTx.FinalizedTokenTransactionHash, coordinatorSig)
	require.ErrorContains(t, s.handler.Commit(s.ctx, commitReq), "not a CREATE")
}

func TestTokenTransactionFlowCommit_RejectsInsufficientSignatures(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	coordinatorSig := s.prepare(s.ctx, op)

	commitReq := &pbinternal.TokenTransactionCommitRequest{
		FinalTokenTransactionHash: finalHash,
		OperatorTransactionSignatures: []*pbinternal.TokenTransactionOperatorSignature{
			{
				OperatorIdentityPublicKey: s.config.IdentityPublicKey().Serialize(),
				Signature:                 coordinatorSig,
			},
		},
	}
	require.ErrorContains(t, s.handler.Commit(s.ctx, commitReq), "signatures")

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusSigned, tx.Status)
}

func TestTokenTransactionFlowCommit_RejectsInvalidSignature(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	coordinatorSig := s.prepare(s.ctx, op)

	commitReq := s.buildCommitRequest(finalHash, coordinatorSig)
	// Corrupt the other operator's signature: sign the wrong message.
	commitReq.OperatorTransactionSignatures[1].Signature = ecdsa.Sign(s.otherOperatorKey.ToBTCEC(), s.fixtures.RandomBytes(32)).Serialize()
	require.ErrorContains(t, s.handler.Commit(s.ctx, commitReq), "failed to verify operator signatures")

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusSigned, tx.Status)
}

func TestTokenTransactionFlowRollback_DeletesRowsAndFreesIdentifier(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	s.prepare(s.ctx, op)
	tokenIdentifier := s.fetchTransaction(finalHash).Edges.Create.TokenIdentifier

	require.NoError(t, s.handler.Rollback(s.ctx, &pbinternal.TokenTransactionRollbackRequest{
		FinalTokenTransactionHash: finalHash,
	}))

	txCount, err := s.client.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalHash)).
		Count(s.ctx)
	require.NoError(t, err)
	assert.Zero(t, txCount)
	createCount, err := s.client.TokenCreate.Query().
		Where(tokencreate.TokenIdentifierEQ(tokenIdentifier)).
		Count(s.ctx)
	require.NoError(t, err)
	assert.Zero(t, createCount)

	// The identifier is free again: the identical prepare op succeeds.
	s.prepare(s.ctx, op)
}

func TestTokenTransactionFlowRollback_IdempotentAndMissingRowNoOp(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	s.prepare(s.ctx, op)

	rollbackReq := &pbinternal.TokenTransactionRollbackRequest{FinalTokenTransactionHash: finalHash}
	require.NoError(t, s.handler.Rollback(s.ctx, rollbackReq))
	require.NoError(t, s.handler.Rollback(s.ctx, rollbackReq))

	// A rollback for a transaction that never prepared is also a no-op.
	require.NoError(t, s.handler.Rollback(s.ctx, &pbinternal.TokenTransactionRollbackRequest{
		FinalTokenTransactionHash: s.fixtures.RandomBytes(32),
	}))
}

func TestTokenTransactionFlowRollback_PostCommitIsNoOp(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	coordinatorSig := s.prepare(s.ctx, op)
	require.NoError(t, s.handler.Commit(s.ctx, s.buildCommitRequest(finalHash, coordinatorSig)))

	require.NoError(t, s.handler.Rollback(s.ctx, &pbinternal.TokenTransactionRollbackRequest{
		FinalTokenTransactionHash: finalHash,
	}))

	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusFinalized, tx.Status)
	require.NotNil(t, tx.Edges.Create)
}

func TestTokenTransactionFlowRollback_AcceptsEchoedPrepareOp(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	s.prepare(s.ctx, op)

	// The participant reconciler's presumed-abort path dispatches the
	// persisted prepare op as the rollback decision.
	require.NoError(t, s.handler.Rollback(s.ctx, op))

	txCount, err := s.client.TokenTransaction.Query().
		Where(tokentransaction.FinalizedTokenTransactionHash(finalHash)).
		Count(s.ctx)
	require.NoError(t, err)
	assert.Zero(t, txCount)
}

func TestTokenTransactionFlowRollback_RejectsWrongOpType(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	require.ErrorContains(t,
		s.handler.Rollback(s.ctx, &pbinternal.TokenTransactionPrepareResponse{}),
		"unexpected operation type")
}

func TestTokenTransactionFlowRollback_ReferencedTokenCreateIsCancelledNotDeleted(t *testing.T) {
	// Each case creates one of the reference types that makes deleting the
	// TokenCreate row unsafe (a consumer racing the aborted create in the
	// SIGNED->rollback window): a mint's outputs, a freeze, or an L1 announce.
	cases := []struct {
		name   string
		addRef func(s *tokenTransactionFlowTestSetup, tokenCreateEnt *ent.TokenCreate, issuerKey keys.Private)
	}{
		{
			name: "mint output",
			addRef: func(s *tokenTransactionFlowTestSetup, tokenCreateEnt *ent.TokenCreate, issuerKey keys.Private) {
				s.fixtures.CreateMintTransaction(
					tokenCreateEnt,
					entfixtures.OutputSpecsWithOwner(issuerKey.Public(), big.NewInt(5)),
					st.TokenTransactionStatusSigned,
				)
			},
		},
		{
			name: "freeze",
			addRef: func(s *tokenTransactionFlowTestSetup, tokenCreateEnt *ent.TokenCreate, _ keys.Private) {
				_, err := s.client.TokenFreeze.Create().
					SetStatus(st.TokenFreezeStatusFrozen).
					SetIssuerSignature(s.fixtures.RandomBytes(64)).
					SetWalletProvidedFreezeTimestamp(1000).
					SetTokenCreateID(tokenCreateEnt.ID).
					Save(s.ctx)
				require.NoError(s.t, err)
			},
		},
		{
			name: "l1 announce",
			addRef: func(s *tokenTransactionFlowTestSetup, tokenCreateEnt *ent.TokenCreate, _ keys.Private) {
				txid, err := st.NewTxIDFromBytes(s.fixtures.RandomBytes(32))
				require.NoError(s.t, err)
				l1Ent, err := s.client.L1TokenCreate.Create().
					SetIssuerPublicKey(tokenCreateEnt.IssuerPublicKey).
					SetTokenName(tokenCreateEnt.TokenName).
					SetTokenTicker(tokenCreateEnt.TokenTicker).
					SetDecimals(tokenCreateEnt.Decimals).
					SetMaxSupply(tokenCreateEnt.MaxSupply).
					SetIsFreezable(tokenCreateEnt.IsFreezable).
					SetNetwork(tokenCreateEnt.Network).
					SetTokenIdentifier(tokenCreateEnt.TokenIdentifier).
					SetTransactionID(txid).
					Save(s.ctx)
				require.NoError(s.t, err)
				_, err = s.client.TokenCreate.UpdateOne(tokenCreateEnt).SetL1TokenCreate(l1Ent).Save(s.ctx)
				require.NoError(s.t, err)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := setUpTokenTransactionFlowTest(t)
			issuerKey := s.fixtures.GeneratePrivateKey()
			op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
			s.prepare(s.ctx, op)

			c.addRef(s, s.fetchTransaction(finalHash).Edges.Create, issuerKey)

			require.NoError(t, s.handler.Rollback(s.ctx, &pbinternal.TokenTransactionRollbackRequest{
				FinalTokenTransactionHash: finalHash,
			}))

			tx := s.fetchTransaction(finalHash)
			assert.Equal(t, st.TokenTransactionStatusSignedCancelled, tx.Status)
			require.NotNil(t, tx.Edges.Create)
		})
	}
}

// TestCreateSignedTransactionEntities_DuplicateIdentifierRaceIsAlreadyExists
// pins the insert-race branch the handler pre-checks cannot cover: a
// concurrent identical create that passed the read checks before the winner's
// insert committed hits the unique token_identifier constraint, which must
// surface as AlreadyExists rather than an internal write error. The entity
// path is driven directly because the branch is unreachable deterministically
// through Prepare (its existence pre-checks would catch the duplicate first).
func TestCreateSignedTransactionEntities_DuplicateIdentifierRaceIsAlreadyExists(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	firstOp, _ := s.buildCreatePrepareOp(issuerKey, "Test Token")
	s.prepare(s.ctx, firstOp)

	// Same metadata (same deterministic token identifier), fresh timestamp so
	// the transaction hashes differ.
	secondOp, _ := s.buildCreatePrepareOp(issuerKey, "Test Token")
	secondTx, ok := proto.Clone(secondOp.GetFinalTokenTransaction()).(*tokenpb.TokenTransaction)
	require.True(t, ok)

	_, err := ent.CreateSignedTransactionEntities(
		s.ctx,
		secondTx,
		secondOp.GetTokenTransactionSignatures(),
		nil,
		nil,
		s.config.IdentityPublicKey(),
		s.fixtures.RandomBytes(64),
	)
	require.ErrorContains(t, err, "token create already exists")
}

func TestTokenTransactionFlowBuildCommitPayload_Success(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")

	finalForResponse, err := protoconverter.ConvertV2TxShapeToFinal(op.GetFinalTokenTransaction())
	require.NoError(t, err)
	flow := newTokenTransactionCoordinatorFlow(
		s.config,
		op.GetFinalTokenTransaction(),
		finalHash,
		op.GetTokenTransactionSignatures(),
		nil,
		finalForResponse,
	)

	// The engine runs the coordinator's own Prepare with PrepareOp() before
	// aggregating results; mirror that here.
	coordinatorResult, err := flow.Prepare(s.ctx, flow.PrepareOp())
	require.NoError(t, err)
	coordinatorAny, err := anypb.New(coordinatorResult)
	require.NoError(t, err)
	otherAny, err := anypb.New(&pbinternal.TokenTransactionPrepareResponse{
		SparkOperatorSignature: ecdsa.Sign(s.otherOperatorKey.ToBTCEC(), finalHash).Serialize(),
	})
	require.NoError(t, err)

	commitMsg, err := flow.BuildCommitPayload(s.ctx, map[string]*anypb.Any{
		s.coordinatorID:   coordinatorAny,
		s.otherOperatorID: otherAny,
	})
	require.NoError(t, err)

	commitReq, ok := commitMsg.(*pbinternal.TokenTransactionCommitRequest)
	require.True(t, ok)
	assert.Equal(t, finalHash, commitReq.GetFinalTokenTransactionHash())
	assert.Len(t, commitReq.GetOperatorTransactionSignatures(), 2)

	// The coordinator applied its own commit in-request: FINALIZED locally.
	tx := s.fetchTransaction(finalHash)
	assert.Equal(t, st.TokenTransactionStatusFinalized, tx.Status)

	require.NotNil(t, flow.response)
	assert.Equal(t, tokenpb.CommitStatus_COMMIT_FINALIZED, flow.response.GetCommitStatus())
	assert.Equal(t, tx.Edges.Create.TokenIdentifier, flow.response.GetTokenIdentifier())
	assert.True(t, proto.Equal(finalForResponse, flow.response.GetFinalTokenTransaction()))
}

func TestTokenTransactionFlowBuildCommitPayload_MissingResultIsError(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	flow := newTokenTransactionCoordinatorFlow(
		s.config, op.GetFinalTokenTransaction(), finalHash, op.GetTokenTransactionSignatures(), nil, nil,
	)

	_, err := flow.BuildCommitPayload(s.ctx, map[string]*anypb.Any{
		s.coordinatorID: nil,
	})
	require.ErrorContains(t, err, "no prepare result")
}

func TestTokenTransactionFlowValidateDecisionAgainstPrepare(t *testing.T) {
	s := setUpTokenTransactionFlowTest(t)
	issuerKey := s.fixtures.GeneratePrivateKey()
	op, finalHash := s.buildCreatePrepareOp(issuerKey, "Test Token")
	otherOp, otherHash := s.buildCreatePrepareOp(issuerKey, "Other Token")

	commitSigs := []*pbinternal.TokenTransactionOperatorSignature{
		{
			OperatorIdentityPublicKey: s.otherOperatorKey.Public().Serialize(),
			Signature:                 ecdsa.Sign(s.otherOperatorKey.ToBTCEC(), finalHash).Serialize(),
		},
	}

	t.Run("commit with matching hash passes", func(t *testing.T) {
		require.NoError(t, s.handler.ValidateDecisionAgainstPrepare(op, &pbinternal.TokenTransactionCommitRequest{
			FinalTokenTransactionHash:     finalHash,
			OperatorTransactionSignatures: commitSigs,
		}))
	})
	t.Run("commit with mismatched hash fails", func(t *testing.T) {
		require.ErrorContains(t, s.handler.ValidateDecisionAgainstPrepare(op, &pbinternal.TokenTransactionCommitRequest{
			FinalTokenTransactionHash:     otherHash,
			OperatorTransactionSignatures: commitSigs,
		}), "does not match")
	})
	t.Run("commit with no signatures fails", func(t *testing.T) {
		require.ErrorContains(t, s.handler.ValidateDecisionAgainstPrepare(op, &pbinternal.TokenTransactionCommitRequest{
			FinalTokenTransactionHash: finalHash,
		}), "no operator signatures")
	})
	t.Run("rollback with matching hash passes", func(t *testing.T) {
		require.NoError(t, s.handler.ValidateDecisionAgainstPrepare(op, &pbinternal.TokenTransactionRollbackRequest{
			FinalTokenTransactionHash: finalHash,
		}))
	})
	t.Run("rollback with mismatched hash fails", func(t *testing.T) {
		require.ErrorContains(t, s.handler.ValidateDecisionAgainstPrepare(op, &pbinternal.TokenTransactionRollbackRequest{
			FinalTokenTransactionHash: otherHash,
		}), "does not match")
	})
	t.Run("echoed prepare op as rollback decision passes", func(t *testing.T) {
		require.NoError(t, s.handler.ValidateDecisionAgainstPrepare(op, op))
	})
	t.Run("echoed foreign prepare op fails", func(t *testing.T) {
		require.ErrorContains(t, s.handler.ValidateDecisionAgainstPrepare(op, otherOp), "does not match")
	})
	t.Run("unexpected decision type fails", func(t *testing.T) {
		require.ErrorContains(t, s.handler.ValidateDecisionAgainstPrepare(op, &pbinternal.TokenTransactionPrepareResponse{}), "unexpected decision op type")
	})
	t.Run("unexpected prepare type fails", func(t *testing.T) {
		require.ErrorContains(t, s.handler.ValidateDecisionAgainstPrepare(&pbinternal.TokenTransactionPrepareResponse{}, op), "unexpected prepare op type")
	})
}
