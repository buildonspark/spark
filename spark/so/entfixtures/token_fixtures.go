package entfixtures

import (
	"math/big"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uint128"
	sparkpb "github.com/lightsparkdev/spark/proto/spark"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/utils"
)

// Token-specific fixture methods

const (
	testWithdrawBondSats              = 1000000
	testWithdrawRelativeBlockLocktime = 1000
)

// OutputSpec specifies how to create a token output
type OutputSpec struct {
	ID                    uuid.UUID // zero value means generate random ID
	Amount                *big.Int
	Owner                 keys.Public // zero value means generate random owner
	RevocationCommitment  []byte      // nil means use random keyshare public key
	BondSats              uint64      // 0 means use default testWithdrawBondSats
	RelativeBlockLocktime uint64      // 0 means use default testWithdrawRelativeBlockLocktime
	FinalizedTxHash       []byte      // nil means use transaction's hash
}

// OutputSpecs creates OutputSpec slice from amounts with random owners
func OutputSpecs(amounts ...*big.Int) []OutputSpec {
	specs := make([]OutputSpec, len(amounts))
	for i, amount := range amounts {
		specs[i] = OutputSpec{Amount: amount}
	}
	return specs
}

// OutputSpecsWithOwner creates OutputSpec slice from amounts with a specific owner
func OutputSpecsWithOwner(owner keys.Public, amounts ...*big.Int) []OutputSpec {
	specs := make([]OutputSpec, len(amounts))
	for i, amount := range amounts {
		specs[i] = OutputSpec{Amount: amount, Owner: owner}
	}
	return specs
}

// TokenCreateOpts specifies options for creating a TokenCreate entity.
type TokenCreateOpts struct {
	IssuerKey       keys.Private // If zero, generates a random key
	TokenIdentifier []byte       // If nil, generates random bytes
	MaxSupply       *big.Int     // If nil, uses default of 1000000
	IsFreezable     bool
}

// TokenTransactionOpts specifies options for creating a token transaction (mint or create).
type TokenTransactionOpts struct {
	Hash       []byte     // If nil, GetHash will generate random bytes
	ExpiryTime *time.Time // If nil, no expiry time is set
}

// GetHash returns the hash, generating and caching a random one if not set.
func (o *TokenTransactionOpts) GetHash(f *Fixtures) []byte {
	if o.Hash == nil {
		o.Hash = f.RandomBytes(32)
	}
	return o.Hash
}

// CreateTokenCreate creates a test TokenCreate entity
func (f *Fixtures) CreateTokenCreate(network btcnetwork.Network, tokenIdentifier []byte, maxSupply *big.Int) *ent.TokenCreate {
	_, tokenCreate := f.CreateTokenCreateWithIssuer(network, tokenIdentifier, maxSupply)
	return tokenCreate
}

// CreateTokenCreateWithIssuer creates a test TokenCreate entity and returns the issuer private key.
// This also creates the entity DKG key to set the proper CreationEntityPublicKey.
// This is useful when you need to sign transactions with the issuer key.
func (f *Fixtures) CreateTokenCreateWithIssuer(network btcnetwork.Network, tokenIdentifier []byte, maxSupply *big.Int) (keys.Private, *ent.TokenCreate) {
	return f.CreateTokenCreateWithOpts(network, TokenCreateOpts{
		TokenIdentifier: tokenIdentifier,
		MaxSupply:       maxSupply,
	})
}

// CreateTokenCreateWithOpts creates a test TokenCreate entity with custom options.
func (f *Fixtures) CreateTokenCreateWithOpts(network btcnetwork.Network, opts TokenCreateOpts) (keys.Private, *ent.TokenCreate) {
	tokenIdentifier := opts.TokenIdentifier
	if tokenIdentifier == nil {
		tokenIdentifier = f.RandomBytes(32)
	}
	maxSupply := opts.MaxSupply
	if maxSupply == nil {
		maxSupply = big.NewInt(1000000)
	}
	issuerKey := opts.IssuerKey
	if issuerKey.IsZero() {
		issuerKey = f.GeneratePrivateKey()
	}

	creationEntityPubKey := f.getOrCreateEntityDkgKeyPublicKey()

	tokenCreate, err := f.Client.TokenCreate.Create().
		SetIssuerPublicKey(issuerKey.Public()).
		SetTokenName("Test Token").
		SetTokenTicker("TST").
		SetDecimals(8).
		SetMaxSupply(maxSupply.Bytes()).
		SetIsFreezable(opts.IsFreezable).
		SetNetwork(network).
		SetTokenIdentifier(tokenIdentifier).
		SetCreationEntityPublicKey(creationEntityPubKey).
		Save(f.Ctx)
	f.RequireNoError(err)
	return issuerKey, tokenCreate
}

// getOrCreateEntityDkgKeyPublicKey returns the public key from the existing entity DKG key,
// or creates one if it doesn't exist.
func (f *Fixtures) getOrCreateEntityDkgKeyPublicKey() keys.Public {
	entityDkgKey, err := f.Client.EntityDkgKey.Query().
		WithSigningKeyshare().
		Only(f.Ctx)
	if err == nil {
		return entityDkgKey.Edges.SigningKeyshare.PublicKey
	}

	// Entity DKG key doesn't exist, create one.
	keyshare := f.CreateKeyshareWithEntityDkgKey()
	return keyshare.PublicKey
}

// CreateKeyshare creates a test SigningKeyshare
func (f *Fixtures) CreateKeyshare() *ent.SigningKeyshare {
	keyshareKey := f.GeneratePrivateKey()
	operatorKey := f.GeneratePrivateKey()

	keyshare, err := f.Client.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(f.GeneratePrivateKey()).
		SetPublicShares(map[string]keys.Public{"operator1": operatorKey.Public()}).
		SetPublicKey(keyshareKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(f.Ctx)
	f.RequireNoError(err)
	return keyshare
}

// CreateKeyshareWithEntityDkgKey creates a SigningKeyshare and links it to an EntityDkgKey.
// This is useful for tests that need the entity DKG key to be present.
func (f *Fixtures) CreateKeyshareWithEntityDkgKey() *ent.SigningKeyshare {
	keyshare := f.CreateKeyshare()

	_, err := f.Client.EntityDkgKey.Create().
		SetSigningKeyshare(keyshare).
		Save(f.Ctx)
	f.RequireNoError(err)

	return keyshare
}

// CreateMintTransaction creates a mint transaction with outputs
func (f *Fixtures) CreateMintTransaction(tokenCreate *ent.TokenCreate, outputSpecs []OutputSpec, status st.TokenTransactionStatus) (*ent.TokenTransaction, []*ent.TokenOutput) {
	return f.CreateMintTransactionWithOpts(tokenCreate, outputSpecs, status, &TokenTransactionOpts{})
}

// CreateMintTransactionWithOpts creates a mint transaction with outputs using custom options
func (f *Fixtures) CreateMintTransactionWithOpts(tokenCreate *ent.TokenCreate, outputSpecs []OutputSpec, status st.TokenTransactionStatus, opts *TokenTransactionOpts) (*ent.TokenTransaction, []*ent.TokenOutput) {
	mint, err := f.Client.TokenMint.Create().
		SetIssuerPublicKey(f.GeneratePrivateKey().Public()).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetWalletProvidedTimestamp(uint64(time.Now().UnixMilli())).
		SetIssuerSignature(f.RandomBytes(64)).
		Save(f.Ctx)
	f.RequireNoError(err)

	hash := opts.GetHash(f)
	finalizedHash := f.RandomBytes(32)

	txBuilder := f.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(finalizedHash).
		SetStatus(status).
		SetMint(mint)
	if opts.ExpiryTime != nil {
		txBuilder = txBuilder.SetExpiryTime(*opts.ExpiryTime)
	}
	tx, err := txBuilder.Save(f.Ctx)
	f.RequireNoError(err)

	outputs := make([]*ent.TokenOutput, len(outputSpecs))
	for i, spec := range outputSpecs {
		outputs[i] = f.createOutputFromSpec(tokenCreate, spec, tx, int32(i))
	}

	return tx, outputs
}

// CreateCreateTransaction creates a CREATE transaction (no outputs) with optional custom hash
func (f *Fixtures) CreateCreateTransaction(tokenCreate *ent.TokenCreate, status st.TokenTransactionStatus, opts *TokenTransactionOpts) *ent.TokenTransaction {
	hash := opts.GetHash(f)
	finalizedHash := f.RandomBytes(32)

	tx, err := f.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(hash).
		SetFinalizedTokenTransactionHash(finalizedHash).
		SetStatus(status).
		SetCreateID(tokenCreate.ID).
		Save(f.Ctx)
	f.RequireNoError(err)

	return tx
}

// CreateOutputForTransaction creates an output linked to a transaction with a random owner
func (f *Fixtures) CreateOutputForTransaction(tokenCreate *ent.TokenCreate, amount *big.Int, tx *ent.TokenTransaction, vout int32) *ent.TokenOutput {
	return f.createOutputFromSpec(tokenCreate, OutputSpec{Amount: amount}, tx, vout)
}

func (f *Fixtures) createOutputFromSpec(tokenCreate *ent.TokenCreate, spec OutputSpec, tx *ent.TokenTransaction, vout int32) *ent.TokenOutput {
	owner := spec.Owner
	if owner.IsZero() {
		owner = f.GeneratePrivateKey().Public()
	}

	keyshare := f.CreateKeyshare()

	revocationCommitment := spec.RevocationCommitment
	if revocationCommitment == nil {
		revocationCommitment = keyshare.PublicKey.Serialize()
	}

	bondSats := spec.BondSats
	if bondSats == 0 {
		bondSats = testWithdrawBondSats
	}

	relativeBlockLocktime := spec.RelativeBlockLocktime
	if relativeBlockLocktime == 0 {
		relativeBlockLocktime = testWithdrawRelativeBlockLocktime
	}

	finalizedTxHash := spec.FinalizedTxHash
	if finalizedTxHash == nil {
		finalizedTxHash = tx.FinalizedTokenTransactionHash
	}

	var outputStatus st.TokenOutputStatus
	switch tx.Status {
	case st.TokenTransactionStatusStarted:
		outputStatus = st.TokenOutputStatusCreatedStarted
	case st.TokenTransactionStatusSigned, st.TokenTransactionStatusRevealed:
		outputStatus = st.TokenOutputStatusCreatedSigned
	case st.TokenTransactionStatusFinalized:
		outputStatus = st.TokenOutputStatusCreatedFinalized
	default:
		outputStatus = st.TokenOutputStatusCreatedStarted
	}
	amountBytes := make([]byte, 16)
	spec.Amount.FillBytes(amountBytes)
	u128Amount, err := uint128.FromBytes(amountBytes)
	f.RequireNoError(err)

	builder := f.Client.TokenOutput.Create().
		SetStatus(outputStatus).
		SetOwnerPublicKey(owner).
		SetWithdrawBondSats(bondSats).
		SetWithdrawRelativeBlockLocktime(relativeBlockLocktime).
		SetWithdrawRevocationCommitment(revocationCommitment).
		SetTokenAmount(amountBytes).
		SetAmount(u128Amount).
		SetCreatedTransactionOutputVout(vout).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreate(tokenCreate).
		SetRevocationKeyshare(keyshare).
		SetNetwork(tokenCreate.Network).
		SetOutputCreatedTokenTransaction(tx).
		SetCreatedTransactionFinalizedHash(finalizedTxHash)

	if spec.ID != uuid.Nil {
		builder = builder.SetID(spec.ID)
	}

	output, err := builder.Save(f.Ctx)
	f.RequireNoError(err)
	return output
}

// CreateStandaloneOutput creates an output not linked to any transaction
func (f *Fixtures) CreateStandaloneOutput(tokenCreate *ent.TokenCreate, amount *big.Int) *ent.TokenOutput {
	_, outputs := f.CreateMintTransaction(tokenCreate, OutputSpecs(amount), st.TokenTransactionStatusFinalized)
	return outputs[0]
}

// CreateBalancedTransferTransaction creates a balanced transfer transaction
func (f *Fixtures) CreateBalancedTransferTransaction(
	tokenCreate *ent.TokenCreate,
	inputs []*ent.TokenOutput,
	outputSpecs []OutputSpec,
	status st.TokenTransactionStatus,
) (*ent.TokenTransaction, []*ent.TokenOutput) {
	tx, err := f.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(f.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(f.RandomBytes(32)).
		SetStatus(st.TokenTransactionStatusSigned).
		Save(f.Ctx)
	f.RequireNoError(err)

	for i, input := range inputs {
		var inputStatus st.TokenOutputStatus
		switch status {
		case st.TokenTransactionStatusStarted:
			inputStatus = st.TokenOutputStatusSpentStarted
		case st.TokenTransactionStatusSigned:
			inputStatus = st.TokenOutputStatusSpentSigned
		case st.TokenTransactionStatusRevealed, st.TokenTransactionStatusFinalized:
			inputStatus = st.TokenOutputStatusSpentFinalized
		default:
			inputStatus = st.TokenOutputStatusSpentStarted
		}

		_, err = input.Update().
			SetOutputSpentTokenTransaction(tx).
			AddOutputSpentStartedTokenTransactions(tx).
			SetStatus(inputStatus).
			SetSpentTransactionInputVout(int32(i)).
			Save(f.Ctx)
		f.RequireNoError(err)
	}

	outputs := make([]*ent.TokenOutput, len(outputSpecs))
	for i, spec := range outputSpecs {
		outputs[i] = f.createOutputFromSpec(tokenCreate, spec, tx, int32(i))
	}

	tx, err = tx.Update().
		SetStatus(status).
		Save(f.Ctx)
	f.RequireNoError(err)

	return tx, outputs
}

// TransferTransactionWithProtoResult holds the result of CreateTransferTransactionWithProto.
type TransferTransactionWithProtoResult struct {
	Transaction *ent.TokenTransaction
	Outputs     []*ent.TokenOutput
	Proto       *tokenpb.TokenTransaction
	Hash        []byte
}

// TransferTransactionOpts specifies options for creating a transfer transaction with proto.
type TransferTransactionOpts struct {
	OperatorPublicKeys []keys.Public // Required: operator identity public keys for the proto
	Status             st.TokenTransactionStatus
}

// CreateTransferTransactionWithProto creates a transfer transaction with a proto that hashes to match.
// This is useful for tests that need to validate proto hashing (e.g., ExchangeRevocationSecretsShares).
// The proto is built first, hashed, then DB entities are created with that hash.
func (f *Fixtures) CreateTransferTransactionWithProto(
	tokenCreate *ent.TokenCreate,
	inputs []*ent.TokenOutput,
	outputSpecs []OutputSpec,
	opts TransferTransactionOpts,
) *TransferTransactionWithProtoResult {
	status := opts.Status
	if status == "" {
		status = st.TokenTransactionStatusSigned
	}

	populatedSpecs := make([]OutputSpec, len(outputSpecs))
	for i, spec := range outputSpecs {
		populatedSpecs[i] = spec
		if populatedSpecs[i].ID == uuid.Nil {
			populatedSpecs[i].ID = uuid.New()
		}
		if populatedSpecs[i].Owner.IsZero() {
			populatedSpecs[i].Owner = f.GeneratePrivateKey().Public()
		}
		if populatedSpecs[i].RevocationCommitment == nil {
			populatedSpecs[i].RevocationCommitment = f.GeneratePrivateKey().Public().Serialize()
		}
		if populatedSpecs[i].BondSats == 0 {
			populatedSpecs[i].BondSats = testWithdrawBondSats
		}
		if populatedSpecs[i].RelativeBlockLocktime == 0 {
			populatedSpecs[i].RelativeBlockLocktime = testWithdrawRelativeBlockLocktime
		}
	}

	protoOutputs := make([]*tokenpb.TokenOutput, len(populatedSpecs))
	for i, spec := range populatedSpecs {
		outputID := spec.ID.String()
		amountBytes := make([]byte, 16)
		spec.Amount.FillBytes(amountBytes)

		protoOutputs[i] = &tokenpb.TokenOutput{
			Id:                            &outputID,
			TokenIdentifier:               tokenCreate.TokenIdentifier,
			OwnerPublicKey:                spec.Owner.Serialize(),
			TokenAmount:                   amountBytes,
			RevocationCommitment:          spec.RevocationCommitment,
			WithdrawBondSats:              &spec.BondSats,
			WithdrawRelativeBlockLocktime: &spec.RelativeBlockLocktime,
		}
	}

	protoInputs := make([]*tokenpb.TokenOutputToSpend, len(inputs))
	for i, input := range inputs {
		protoInputs[i] = &tokenpb.TokenOutputToSpend{
			PrevTokenTransactionHash: input.CreatedTransactionFinalizedHash,
			PrevTokenTransactionVout: uint32(input.CreatedTransactionOutputVout),
		}
	}

	operatorPubKeysBytes := make([][]byte, len(opts.OperatorPublicKeys))
	for i, pk := range opts.OperatorPublicKeys {
		operatorPubKeysBytes[i] = pk.Serialize()
	}

	validityDuration := uint64(3600)
	tokenTxProto := &tokenpb.TokenTransaction{
		Version: 3,
		TokenInputs: &tokenpb.TokenTransaction_TransferInput{
			TransferInput: &tokenpb.TokenTransferInput{
				OutputsToSpend: protoInputs,
			},
		},
		TokenOutputs:                    protoOutputs,
		SparkOperatorIdentityPublicKeys: operatorPubKeysBytes,
		Network:                         sparkpb.Network_REGTEST,
		ClientCreatedTimestamp:          timestamppb.New(utils.ToMicrosecondPrecision(time.Now())),
		ValidityDurationSeconds:         &validityDuration,
	}

	finalTxHash, err := utils.HashTokenTransaction(tokenTxProto, false)
	f.RequireNoError(err)

	tx, err := f.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(finalTxHash).
		SetFinalizedTokenTransactionHash(finalTxHash).
		SetStatus(status).
		Save(f.Ctx)
	f.RequireNoError(err)

	for i, input := range inputs {
		var inputStatus st.TokenOutputStatus
		switch status {
		case st.TokenTransactionStatusStarted:
			inputStatus = st.TokenOutputStatusSpentStarted
		case st.TokenTransactionStatusSigned:
			inputStatus = st.TokenOutputStatusSpentSigned
		case st.TokenTransactionStatusRevealed, st.TokenTransactionStatusFinalized:
			inputStatus = st.TokenOutputStatusSpentFinalized
		default:
			inputStatus = st.TokenOutputStatusSpentStarted
		}

		_, err = input.Update().
			SetOutputSpentTokenTransaction(tx).
			AddOutputSpentStartedTokenTransactions(tx).
			SetStatus(inputStatus).
			SetSpentTransactionInputVout(int32(i)).
			Save(f.Ctx)
		f.RequireNoError(err)
	}

	outputs := make([]*ent.TokenOutput, len(populatedSpecs))
	for i, spec := range populatedSpecs {
		spec.FinalizedTxHash = finalTxHash
		outputs[i] = f.createOutputFromSpec(tokenCreate, spec, tx, int32(i))
	}

	return &TransferTransactionWithProtoResult{
		Transaction: tx,
		Outputs:     outputs,
		Proto:       tokenTxProto,
		Hash:        finalTxHash,
	}
}
