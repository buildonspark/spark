package entfixtures

import (
	"math/big"
	"time"

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
func (f *Fixtures) CreateTokenCreate(network st.Network, tokenIdentifier []byte, maxSupply *big.Int) *ent.TokenCreate {
	if tokenIdentifier == nil {
		tokenIdentifier = f.RandomBytes(32)
	}
	if maxSupply == nil {
		maxSupply = big.NewInt(1000000)
	}

	issuerKey := keys.GeneratePrivateKey()
	creationEntityKey := keys.GeneratePrivateKey()

	tokenCreate, err := f.Tx.TokenCreate.Create().
		SetIssuerPublicKey(issuerKey.Public()).
		SetTokenName("Test Token").
		SetTokenTicker("TST").
		SetDecimals(8).
		SetMaxSupply(maxSupply.Bytes()).
		SetIsFreezable(false).
		SetNetwork(network).
		SetTokenIdentifier(tokenIdentifier).
		SetCreationEntityPublicKey(creationEntityKey.Public()).
		Save(f.Ctx)
	f.RequireNoError(err)
	return tokenCreate
}

// CreateKeyshare creates a test SigningKeyshare
func (f *Fixtures) CreateKeyshare() *ent.SigningKeyshare {
	keyshareKey := keys.GeneratePrivateKey()
	operatorKey := keys.GeneratePrivateKey()

	keyshare, err := f.Tx.SigningKeyshare.Create().
		SetStatus(st.KeyshareStatusAvailable).
		SetSecretShare(keys.GeneratePrivateKey()).
		SetPublicShares(map[string]keys.Public{"operator1": operatorKey.Public()}).
		SetPublicKey(keyshareKey.Public()).
		SetMinSigners(2).
		SetCoordinatorIndex(0).
		Save(f.Ctx)
	f.RequireNoError(err)
	return keyshare
}

// CreateMintTransaction creates a mint transaction with outputs
func (f *Fixtures) CreateMintTransaction(tokenCreate *ent.TokenCreate, outputSpecs []OutputSpec, status st.TokenTransactionStatus) (*ent.TokenTransaction, []*ent.TokenOutput) {
	mint, err := f.Tx.TokenMint.Create().
		SetIssuerPublicKey(keys.GeneratePrivateKey().Public()).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetWalletProvidedTimestamp(uint64(time.Now().UnixMilli())).
		SetIssuerSignature(f.RandomBytes(64)).
		Save(f.Ctx)
	f.RequireNoError(err)

	tx, err := f.Tx.TokenTransaction.Create().
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
		owner = keys.GeneratePrivateKey().Public()
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
	u128Amount := uint128.Uint128{}
	err := u128Amount.SafeSetBytes(amountBytes)
	f.RequireNoError(err)

	output, err := f.Tx.TokenOutput.Create().
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
		Save(f.Ctx)
	f.RequireNoError(err)
	return output
}

// CreateStandaloneOutput creates an output not linked to any transaction
func (f *Fixtures) CreateStandaloneOutput(tokenCreate *ent.TokenCreate, amount *big.Int, status st.TokenOutputStatus) *ent.TokenOutput {
	ownerKey := keys.GeneratePrivateKey()
	keyshare := f.CreateKeyshare()

	amountBytes := make([]byte, 16)
	amount.FillBytes(amountBytes)
	u128Amount := uint128.Uint128{}
	err := u128Amount.SafeSetBytes(amountBytes)
	f.RequireNoError(err)

	output, err := f.Tx.TokenOutput.Create().
		SetStatus(status).
		SetOwnerPublicKey(ownerKey.Public()).
		SetWithdrawBondSats(testWithdrawBondSats).
		SetWithdrawRelativeBlockLocktime(testWithdrawRelativeBlockLocktime).
		SetWithdrawRevocationCommitment(f.RandomBytes(32)).
		SetTokenAmount(amountBytes).
		SetAmount(u128Amount).
		SetCreatedTransactionOutputVout(0).
		SetTokenIdentifier(tokenCreate.TokenIdentifier).
		SetTokenCreate(tokenCreate).
		SetRevocationKeyshare(keyshare).
		SetNetwork(tokenCreate.Network).
		Save(f.Ctx)
	f.RequireNoError(err)

	return output
}

// CreateBalancedTransferTransaction creates a balanced transfer transaction
func (f *Fixtures) CreateBalancedTransferTransaction(
	tokenCreate *ent.TokenCreate,
	inputs []*ent.TokenOutput,
	outputSpecs []OutputSpec,
	status st.TokenTransactionStatus,
) (*ent.TokenTransaction, []*ent.TokenOutput) {
	tx, err := f.Tx.TokenTransaction.Create().
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
