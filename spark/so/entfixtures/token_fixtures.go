package entfixtures

import (
	"math/big"
	"time"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/uint128"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// Token-specific fixture methods

const (
	testWithdrawBondSats              = 1000000
	testWithdrawRelativeBlockLocktime = 1000
)

// OutputSpec specifies how to create a token output
type OutputSpec struct {
	Amount *big.Int
	Owner  keys.Public // zero value means generate random owner
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

// CreateTokenCreate creates a test TokenCreate entity
func (f *Fixtures) CreateTokenCreate(network btcnetwork.Network, tokenIdentifier []byte, maxSupply *big.Int) *ent.TokenCreate {
	_, tokenCreate := f.CreateTokenCreateWithIssuer(network, tokenIdentifier, maxSupply)
	return tokenCreate
}

// CreateTokenCreateWithIssuer creates a test TokenCreate entity and returns the issuer private key.
// This also creates the entity DKG key to set the proper CreationEntityPublicKey.
// This is useful when you need to sign transactions with the issuer key.
func (f *Fixtures) CreateTokenCreateWithIssuer(network btcnetwork.Network, tokenIdentifier []byte, maxSupply *big.Int) (keys.Private, *ent.TokenCreate) {
	if tokenIdentifier == nil {
		tokenIdentifier = f.RandomBytes(32)
	}
	if maxSupply == nil {
		maxSupply = big.NewInt(1000000)
	}

	issuerKey := f.GeneratePrivateKey()

	// Get or create the entity DKG key for CreationEntityPublicKey.
	creationEntityPubKey := f.getOrCreateEntityDkgKeyPublicKey()

	tokenCreate, err := f.Client.TokenCreate.Create().
		SetIssuerPublicKey(issuerKey.Public()).
		SetTokenName("Test Token").
		SetTokenTicker("TST").
		SetDecimals(8).
		SetMaxSupply(maxSupply.Bytes()).
		SetIsFreezable(false).
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
	mint, err := f.Client.TokenMint.Create().
		SetIssuerPublicKey(f.GeneratePrivateKey().Public()).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetWalletProvidedTimestamp(uint64(time.Now().UnixMilli())).
		SetIssuerSignature(f.RandomBytes(64)).
		Save(f.Ctx)
	f.RequireNoError(err)

	tx, err := f.Client.TokenTransaction.Create().
		SetPartialTokenTransactionHash(f.RandomBytes(32)).
		SetFinalizedTokenTransactionHash(f.RandomBytes(32)).
		SetStatus(status).
		SetMint(mint).
		Save(f.Ctx)
	f.RequireNoError(err)

	outputs := make([]*ent.TokenOutput, len(outputSpecs))
	for i, spec := range outputSpecs {
		outputs[i] = f.createOutputForTransactionWithOwner(tokenCreate, spec.Amount, spec.Owner, tx, int32(i))
	}

	return tx, outputs
}

// CreateOutputForTransaction creates an output linked to a transaction with a random owner
func (f *Fixtures) CreateOutputForTransaction(tokenCreate *ent.TokenCreate, amount *big.Int, tx *ent.TokenTransaction, vout int32) *ent.TokenOutput {
	return f.createOutputForTransactionWithOwner(tokenCreate, amount, keys.Public{}, tx, vout)
}

// createOutputForTransactionWithOwner creates an output linked to a transaction with an optional owner (zero value = random)
func (f *Fixtures) createOutputForTransactionWithOwner(tokenCreate *ent.TokenCreate, amount *big.Int, owner keys.Public, tx *ent.TokenTransaction, vout int32) *ent.TokenOutput {
	// Generate random owner if not provided
	if owner.IsZero() {
		owner = f.GeneratePrivateKey().Public()
	}

	keyshare := f.CreateKeyshare()

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
	amount.FillBytes(amountBytes)
	u128Amount, err := uint128.FromBytes(amountBytes)
	f.RequireNoError(err)

	output, err := f.Client.TokenOutput.Create().
		SetStatus(outputStatus).
		SetOwnerPublicKey(owner).
		SetWithdrawBondSats(testWithdrawBondSats).
		SetWithdrawRelativeBlockLocktime(testWithdrawRelativeBlockLocktime).
		SetWithdrawRevocationCommitment(f.RandomBytes(32)).
		SetTokenAmount(amountBytes).
		SetAmount(u128Amount).
		SetCreatedTransactionOutputVout(vout).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreate(tokenCreate).
		SetRevocationKeyshare(keyshare).
		SetNetwork(tokenCreate.Network).
		SetOutputCreatedTokenTransaction(tx).
		SetCreatedTransactionFinalizedHash(tx.FinalizedTokenTransactionHash).
		Save(f.Ctx)
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
		outputs[i] = f.createOutputForTransactionWithOwner(tokenCreate, spec.Amount, spec.Owner, tx, int32(i))
	}

	tx, err = tx.Update().
		SetStatus(status).
		Save(f.Ctx)
	f.RequireNoError(err)

	return tx, outputs
}
