package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/sighash"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/frost"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

const (
	DepositTimeout      = 30 * time.Second
	DepositPollInterval = 100 * time.Millisecond
)

type CreateRootFlow struct {
	Name       string
	CreateRoot func(ctx context.Context,
		config *TestWalletConfig,
		signingPrivKey keys.Private,
		verifyingKey keys.Public,
		depositTx *wire.MsgTx,
		vout int,
	) ([]*pb.TreeNode, error)
}

var CreateRootFlows = []CreateRootFlow{
	{
		Name: "original flow",
		CreateRoot: func(ctx context.Context,
			config *TestWalletConfig,
			signingPrivKey keys.Private,
			verifyingKey keys.Public,
			depositTx *wire.MsgTx,
			vout int,
		) ([]*pb.TreeNode, error) {
			res, err := CreateTreeRoot(ctx, config, signingPrivKey, verifyingKey, depositTx, vout, false)
			if err != nil {
				return nil, err
			}
			return res.GetNodes(), nil
		},
	},
	{
		Name: "single mutation flow",
		CreateRoot: func(ctx context.Context,
			config *TestWalletConfig,
			signingPrivKey keys.Private,
			verifyingKey keys.Public,
			depositTx *wire.MsgTx,
			vout int,
		) ([]*pb.TreeNode, error) {
			res, err := CreateTreeRootWithFinalizeDepositTreeCreation(ctx, config, signingPrivKey, verifyingKey, depositTx, vout)
			if err != nil {
				return nil, err
			}
			return []*pb.TreeNode{res.GetRootNode()}, nil
		},
	},
}

// validateDepositAddress validates the cryptographic proofs of a deposit address.
//  1. Proof of keyshare possession signature - ensures that the keyshare is known by all SOs
//  2. Address signatures from all participating signing operators - ensures that all SOs have generated the address
//
// Parameters:
//   - config: Test wallet configuration containing signing operator details
//   - address: The deposit address with its associated cryptographic proofs
//   - signingPubKey: The user's public part of the signing key used in deposit address generation
//   - verifyCoordinatorProof: Whether to verify the coordinator's address signature in addition to the other operator signatures
func validateDepositAddress(config *TestWalletConfig, address *pb.Address, signingPubKey keys.Public, verifyCoordinatorProof bool) error {
	if address.DepositAddressProof.ProofOfPossessionSignature == nil {
		return fmt.Errorf("proof of possession signature is nil")
	}
	verifyingKey, err := keys.ParsePublicKey(address.GetVerifyingKey())
	if err != nil {
		return err
	}
	operatorPubKey := verifyingKey.Sub(signingPubKey)
	msg := common.ProofOfPossessionMessageHashForDepositAddress(config.IdentityPublicKey(), operatorPubKey, []byte(address.GetAddress()), pb.HashVariant_HASH_VARIANT_UNSPECIFIED)
	sig, err := schnorr.ParseSignature(address.GetDepositAddressProof().GetProofOfPossessionSignature())
	if err != nil {
		return err
	}

	taprootKey := txscript.ComputeTaprootKeyNoScript(operatorPubKey.ToBTCEC())

	verified := sig.Verify(msg[:], taprootKey)
	if !verified {
		return fmt.Errorf("signature verification failed")
	}

	if address.DepositAddressProof.AddressSignatures == nil {
		return fmt.Errorf("address signatures is nil")
	}

	addrHash := sha256.Sum256([]byte(address.GetAddress()))
	for _, operator := range config.SigningOperators {
		if operator.Identifier == config.CoordinatorIdentifier && !verifyCoordinatorProof {
			continue
		}

		operatorSig, ok := address.GetDepositAddressProof().GetAddressSignatures()[operator.Identifier]
		if !ok {
			return fmt.Errorf("address signature for operator %s is nil", operator.Identifier)
		}

		sig, err := ecdsa.ParseDERSignature(operatorSig)
		if err != nil {
			return err
		}

		if !operator.IdentityPublicKey.Verify(sig, addrHash[:]) {
			return fmt.Errorf("signature verification failed for operator %s", operator.Identifier)
		}
	}
	return nil
}

// GenerateDepositAddress generates a deposit address for a given identity and signing public key.
func GenerateDepositAddress(
	ctx context.Context,
	config *TestWalletConfig,
	signingPubkey keys.Public,
	// Signing pub key should be generated in a deterministic way from this leaf ID.
	// This will be used as the leaf ID for the leaf node.
	customLeafID *string,
	isStatic bool,
) (*pb.GenerateDepositAddressResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	depositResp, err := sparkClient.GenerateDepositAddress(ctx, &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  signingPubkey.Serialize(),
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		Network:           config.ProtoNetwork(),
		LeafId:            customLeafID,
		IsStatic:          &isStatic,
	})
	if err != nil {
		return nil, err
	}
	if err := validateDepositAddress(config, depositResp.GetDepositAddress(), signingPubkey, false); err != nil {
		return nil, err
	}
	return depositResp, nil
}

// GenerateStaticDepositAddress generates a static deposit address for a given identity and signing public key.
func GenerateStaticDepositAddress(
	ctx context.Context,
	config *TestWalletConfig,
	signingPubKey keys.Public,
) (*pb.GenerateDepositAddressResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	depositResp, err := sparkClient.GenerateDepositAddress(ctx, &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  signingPubKey.Serialize(),
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		Network:           config.ProtoNetwork(),
		IsStatic:          new(true),
	})
	if err != nil {
		return nil, err
	}
	if err := validateDepositAddress(config, depositResp.GetDepositAddress(), signingPubKey, false); err != nil {
		return nil, err
	}
	return depositResp, nil
}

// GenerateStaticDepositAddressDedicatedEndpoint generates a static deposit address for a given identity and signing public key.
func GenerateStaticDepositAddressDedicatedEndpoint(
	ctx context.Context,
	config *TestWalletConfig,
	signingPubKey keys.Public,
) (*pb.GenerateStaticDepositAddressResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	depositResp, err := sparkClient.GenerateStaticDepositAddress(ctx, &pb.GenerateStaticDepositAddressRequest{
		SigningPublicKey:  signingPubKey.Serialize(),
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		Network:           config.ProtoNetwork(),
	})
	if err != nil {
		return nil, err
	}
	if err := validateDepositAddress(config, depositResp.GetDepositAddress(), signingPubKey, true); err != nil {
		return nil, err
	}
	return depositResp, nil
}

// RotateStaticDepositAddress rotates the static deposit address for a given identity and signing public key.
// It archives the current default static deposit address and generates a new one.
func RotateStaticDepositAddress(
	ctx context.Context,
	config *TestWalletConfig,
	signingPubKey keys.Public,
) (*pb.RotateStaticDepositAddressResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	rotateResp, err := sparkClient.RotateStaticDepositAddress(ctx, &pb.RotateStaticDepositAddressRequest{
		SigningPublicKey: signingPubKey.Serialize(),
		Network:          config.ProtoNetwork(),
	})
	if err != nil {
		return nil, err
	}
	if err := validateDepositAddress(config, rotateResp.GetNewDepositAddress(), signingPubKey, true); err != nil {
		return nil, err
	}
	if rotateResp.GetArchivedDepositAddress() != nil {
		if err := validateDepositAddress(config, rotateResp.GetArchivedDepositAddress(), signingPubKey, true); err != nil {
			return nil, err
		}
	}
	return rotateResp, nil
}

func QueryUnusedDepositAddresses(
	ctx context.Context,
	config *TestWalletConfig,
) (*pb.QueryUnusedDepositAddressesResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	network, err := config.Network.ToProtoNetwork()
	if err != nil {
		return nil, fmt.Errorf("failed to get proto network: %w", err)
	}

	var allAddresses []*pb.DepositAddressQueryResult
	offset := int64(0)
	limit := int64(100) // Use reasonable batch size

	for {
		response, err := sparkClient.QueryUnusedDepositAddresses(ctx, &pb.QueryUnusedDepositAddressesRequest{
			IdentityPublicKey: config.IdentityPublicKey().Serialize(),
			Network:           network,
			Limit:             limit,
			Offset:            offset,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to query unused deposit addresses at offset %d: %w", offset, err)
		}

		// Collect results from this page
		allAddresses = append(allAddresses, response.GetDepositAddresses()...)

		// Check if there are more results
		if response.GetOffset() == -1 {
			break // No more results
		}

		offset = response.GetOffset()
	}

	return &pb.QueryUnusedDepositAddressesResponse{
		DepositAddresses: allAddresses,
		Offset:           offset,
	}, nil
}

func QueryStaticDepositAddresses(
	ctx context.Context,
	config *TestWalletConfig,
	signingPubKey keys.Public,
) (*pb.QueryStaticDepositAddressesResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	network, err := config.Network.ToProtoNetwork()
	if err != nil {
		return nil, fmt.Errorf("failed to get proto network: %w", err)
	}
	addresses, err := sparkClient.QueryStaticDepositAddresses(ctx, &pb.QueryStaticDepositAddressesRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		Network:           network,
	})
	if err != nil {
		return nil, err
	}
	for _, address := range addresses.GetDepositAddresses() {
		if err := validateDepositAddress(config, &pb.Address{
			Address:             address.GetDepositAddress(),
			VerifyingKey:        address.GetVerifyingPublicKey(),
			DepositAddressProof: address.GetProofOfPossession(),
		}, signingPubKey, true); err != nil {
			return nil, err
		}
	}
	return addresses, nil
}

// preparedTxSigningArtifacts bundles the common artifacts needed to submit a tx
// for signing and to later include in user signing jobs.
type preparedTxSigningArtifacts struct {
	rawTx      []byte
	sighash    sighash.Hash
	nonce      *pbfrost.SigningNonce
	commitment *pbcommon.SigningCommitment
	signingJob *pb.SigningJob
}

func prepareTxSigningArtifacts(tx *wire.MsgTx, prevTxOut *wire.TxOut, signingPublicKey keys.Public) (*preparedTxSigningArtifacts, error) {
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}

	nonce := frost.GenerateSigningNonce()
	nonceProto, _ := nonce.MarshalProto()
	commitmentProto, _ := nonce.SigningCommitment().MarshalProto()

	txSig, err := sighash.FromTx(tx, 0, prevTxOut)
	if err != nil {
		return nil, err
	}

	job := &pb.SigningJob{
		RawTx:                  buf.Bytes(),
		SigningPublicKey:       signingPublicKey.Serialize(),
		SigningNonceCommitment: commitmentProto,
	}

	return &preparedTxSigningArtifacts{
		rawTx:      buf.Bytes(),
		sighash:    txSig,
		nonce:      nonceProto,
		commitment: commitmentProto,
		signingJob: job,
	}, nil
}

func CreateTreeRoot(
	ctx context.Context,
	config *TestWalletConfig,
	signingPrivKey keys.Private,
	verifyingKey keys.Public,
	depositTx *wire.MsgTx,
	vout int,
	skipFinalizeSignatures bool,
) (*pb.FinalizeNodeSignaturesResponse, error) {
	signingPubKey := signingPrivKey.Public()
	depositOutPoint := &wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)}
	rootTx := createRootTx(depositOutPoint, depositTx.TxOut[0])
	rootPrepared, err := prepareTxSigningArtifacts(rootTx, depositTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}
	var depositBuf bytes.Buffer
	err = depositTx.Serialize(&depositBuf)
	if err != nil {
		return nil, err
	}

	initialRefundSequence, initialDirectSequence := InitialRefundSequences()

	// Create CPFP refund tx
	cpfpRefundTx, _, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	refundPrepared, err := prepareTxSigningArtifacts(cpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	_, directFromCpfpRefundTx, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	treeResponse, err := sparkClient.StartDepositTreeCreation(ctx, &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			Vout:    uint32(vout),
			RawTx:   depositBuf.Bytes(),
			Network: config.ProtoNetwork(),
		},
		RootTxSigningJob:                 rootPrepared.signingJob,
		RefundTxSigningJob:               refundPrepared.signingJob,
		DirectFromCpfpRefundTxSigningJob: directFromCpfpRefundPrepared.signingJob,
	})
	if err != nil {
		return nil, err
	}

	if skipFinalizeSignatures {
		return nil, nil
	}

	rootNodeVerifyingKey, err := keys.ParsePublicKey(treeResponse.GetRootNodeSignatureShares().GetVerifyingKey())
	if err != nil {
		return nil, err
	}
	if !rootNodeVerifyingKey.Equals(verifyingKey) {
		return nil, fmt.Errorf("verifying key does not match")
	}

	userKeyPackage := CreateUserKeyPackage(signingPrivKey)

	nodeJobID := uuid.NewString()
	refundJobID := uuid.NewString()
	directFromCpfpRefundJobID := uuid.NewString()
	userSigningJobs := []*pbfrost.FrostSigningJob{
		{
			JobId:           nodeJobID,
			Message:         rootPrepared.sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           rootPrepared.nonce,
			Commitments:     treeResponse.GetRootNodeSignatureShares().GetNodeTxSigningResult().GetSigningNonceCommitments(),
			UserCommitments: rootPrepared.commitment,
		},
		{
			JobId:           refundJobID,
			Message:         refundPrepared.sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    treeResponse.GetRootNodeSignatureShares().GetVerifyingKey(),
			Nonce:           refundPrepared.nonce,
			Commitments:     treeResponse.GetRootNodeSignatureShares().GetRefundTxSigningResult().GetSigningNonceCommitments(),
			UserCommitments: refundPrepared.commitment,
		},
		{
			JobId:           directFromCpfpRefundJobID,
			Message:         directFromCpfpRefundPrepared.sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    treeResponse.GetRootNodeSignatureShares().GetVerifyingKey(),
			Nonce:           directFromCpfpRefundPrepared.nonce,
			Commitments:     treeResponse.GetRootNodeSignatureShares().GetDirectFromCpfpRefundTxSigningResult().GetSigningNonceCommitments(),
			UserCommitments: directFromCpfpRefundPrepared.commitment,
		},
	}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	rootSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            rootPrepared.sighash.Serialize(),
		SignatureShares:    treeResponse.GetRootNodeSignatureShares().GetNodeTxSigningResult().GetSignatureShares(),
		PublicShares:       treeResponse.GetRootNodeSignatureShares().GetNodeTxSigningResult().GetPublicKeys(),
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.GetRootNodeSignatureShares().GetNodeTxSigningResult().GetSigningNonceCommitments(),
		UserCommitments:    rootPrepared.commitment,
		UserPublicKey:      signingPubKey.Serialize(),
		UserSignatureShare: userSignatures.GetResults()[nodeJobID].GetSignatureShare(),
	})
	if err != nil {
		return nil, err
	}

	refundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            refundPrepared.sighash.Serialize(),
		SignatureShares:    treeResponse.GetRootNodeSignatureShares().GetRefundTxSigningResult().GetSignatureShares(),
		PublicShares:       treeResponse.GetRootNodeSignatureShares().GetRefundTxSigningResult().GetPublicKeys(),
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.GetRootNodeSignatureShares().GetRefundTxSigningResult().GetSigningNonceCommitments(),
		UserCommitments:    refundPrepared.commitment,
		UserPublicKey:      signingPubKey.Serialize(),
		UserSignatureShare: userSignatures.GetResults()[refundJobID].GetSignatureShare(),
	})
	if err != nil {
		return nil, err
	}

	directFromCpfpRefundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            directFromCpfpRefundPrepared.sighash.Serialize(),
		SignatureShares:    treeResponse.GetRootNodeSignatureShares().GetDirectFromCpfpRefundTxSigningResult().GetSignatureShares(),
		PublicShares:       treeResponse.GetRootNodeSignatureShares().GetDirectFromCpfpRefundTxSigningResult().GetPublicKeys(),
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.GetRootNodeSignatureShares().GetDirectFromCpfpRefundTxSigningResult().GetSigningNonceCommitments(),
		UserCommitments:    directFromCpfpRefundPrepared.commitment,
		UserPublicKey:      signingPubKey.Serialize(),
		UserSignatureShare: userSignatures.GetResults()[directFromCpfpRefundJobID].GetSignatureShare(),
	})
	if err != nil {
		return nil, err
	}

	return sparkClient.FinalizeNodeSignaturesV2(ctx, &pb.FinalizeNodeSignaturesRequest{
		Intent: pbcommon.SignatureIntent_CREATION,
		NodeSignatures: []*pb.NodeSignatures{
			{
				NodeId:                          treeResponse.GetRootNodeSignatureShares().GetNodeId(),
				NodeTxSignature:                 rootSignature.GetSignature(),
				RefundTxSignature:               refundSignature.GetSignature(),
				DirectFromCpfpRefundTxSignature: directFromCpfpRefundSignature.GetSignature(),
			},
		},
	})
}

func CreateTreeRootWithFinalizeDepositTreeCreation(
	ctx context.Context,
	config *TestWalletConfig,
	signingPrivKey keys.Private,
	verifyingKey keys.Public,
	depositTx *wire.MsgTx,
	vout int,
) (*pb.FinalizeDepositTreeCreationResponse, error) {
	signingPubKey := signingPrivKey.Public()
	// Create root tx
	depositOutPoint := &wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)}
	rootTx := createRootTx(depositOutPoint, depositTx.TxOut[0])
	rootPrepared, err := prepareTxSigningArtifacts(rootTx, depositTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}
	var depositBuf bytes.Buffer
	err = depositTx.Serialize(&depositBuf)
	if err != nil {
		return nil, err
	}

	initialRefundSequence, initialDirectSequence := InitialRefundSequences()

	// Create CPFP refund tx
	cpfpRefundTx, _, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	refundPrepared, err := prepareTxSigningArtifacts(cpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	// Create Direct-From-CPFP Refund Tx
	_, directFromCpfpRefundTx, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	// Step 1: Get SE commitments (non-mutating call)
	commitmentsResp, err := sparkClient.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		Count:       3,
		NodeIdCount: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get SE commitments: %w", err)
	}

	if len(commitmentsResp.GetSigningCommitments()) != 3 {
		return nil, fmt.Errorf("got %d commitments, expected 3", len(commitmentsResp.GetSigningCommitments()))
	}

	// Step 2: Generate user signature shares using SE commitments
	userKeyPackage := CreateUserKeyPackage(signingPrivKey)

	nodeJobID := uuid.NewString()
	refundJobID := uuid.NewString()
	directFromCpfpRefundJobID := uuid.NewString()
	userSigningJobs := []*pbfrost.FrostSigningJob{
		{
			JobId:           nodeJobID,
			Message:         rootPrepared.sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           rootPrepared.nonce,
			UserCommitments: rootPrepared.commitment,
			Commitments:     commitmentsResp.GetSigningCommitments()[0].GetSigningNonceCommitments(),
		},
		{
			JobId:           refundJobID,
			Message:         refundPrepared.sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           refundPrepared.nonce,
			UserCommitments: refundPrepared.commitment,
			Commitments:     commitmentsResp.GetSigningCommitments()[1].GetSigningNonceCommitments(),
		},
		{
			JobId:           directFromCpfpRefundJobID,
			Message:         directFromCpfpRefundPrepared.sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           directFromCpfpRefundPrepared.nonce,
			UserCommitments: directFromCpfpRefundPrepared.commitment,
			Commitments:     commitmentsResp.GetSigningCommitments()[2].GetSigningNonceCommitments(),
		},
	}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	nodeSignature, ok := userSignatures.GetResults()[nodeJobID]
	if !ok || nodeSignature == nil {
		returnedResults := slices.Collect(maps.Keys(userSignatures.GetResults()))
		return nil, fmt.Errorf("node signature (%s) not returned from frost (returned %s)", nodeJobID, strings.Join(returnedResults, ","))
	}
	refundSignature, ok := userSignatures.GetResults()[refundJobID]
	if !ok || refundSignature == nil {
		returnedResults := slices.Collect(maps.Keys(userSignatures.GetResults()))
		return nil, fmt.Errorf("refund signature (%s) not returned from frost (returned %s)", refundJobID, strings.Join(returnedResults, ","))
	}
	cpfpRefundSignature, ok := userSignatures.GetResults()[directFromCpfpRefundJobID]
	if !ok || cpfpRefundSignature == nil {
		returnedResults := slices.Collect(maps.Keys(userSignatures.GetResults()))
		return nil, fmt.Errorf("cpfp refund signature (%s) not returned from frost (returned %s)", directFromCpfpRefundJobID, strings.Join(returnedResults, ","))
	}

	// Step 3: Call the finalize endpoint with user signature shares and SE commitments
	finalizeResp, err := sparkClient.FinalizeDepositTreeCreation(ctx, &pb.FinalizeDepositTreeCreationRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			Vout:    uint32(vout),
			RawTx:   depositBuf.Bytes(),
			Network: config.ProtoNetwork(),
		},
		RootTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  rootPrepared.signingJob.GetRawTx(),
			SigningNonceCommitment: rootPrepared.signingJob.GetSigningNonceCommitment(),
			UserSignature:          nodeSignature.GetSignatureShare(),
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[0].GetSigningNonceCommitments(),
			},
		},
		RefundTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  refundPrepared.signingJob.GetRawTx(),
			SigningNonceCommitment: refundPrepared.signingJob.GetSigningNonceCommitment(),
			UserSignature:          refundSignature.GetSignatureShare(),
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[1].GetSigningNonceCommitments(),
			},
		},
		DirectFromCpfpRefundTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  directFromCpfpRefundPrepared.signingJob.GetRawTx(),
			SigningNonceCommitment: directFromCpfpRefundPrepared.signingJob.GetSigningNonceCommitment(),
			UserSignature:          cpfpRefundSignature.GetSignatureShare(),
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[2].GetSigningNonceCommitments(),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	return finalizeResp, nil
}

// DepositUTXO represents a single deposit UTXO for multi-UTXO finalization.
type DepositUTXO struct {
	Tx   *wire.MsgTx
	Vout int
}

// prepareTxSigningArtifactsMultiInput computes the sighash for a specific input of a multi-input tx.
func prepareTxSigningArtifactsMultiInput(
	tx *wire.MsgTx,
	inputIndex int,
	prevOutputs map[wire.OutPoint]*wire.TxOut,
	signingPublicKey keys.Public,
) (*preparedTxSigningArtifacts, error) {
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}

	nonce := frost.GenerateSigningNonce()
	nonceProto, _ := nonce.MarshalProto()
	commitmentProto, _ := nonce.SigningCommitment().MarshalProto()

	txSig, err := sighash.FromMultiPrevOutTx(tx, inputIndex, prevOutputs)
	if err != nil {
		return nil, err
	}

	return &preparedTxSigningArtifacts{
		rawTx:      buf.Bytes(),
		sighash:    txSig,
		nonce:      nonceProto,
		commitment: commitmentProto,
	}, nil
}

// createMultiInputRootTx creates a root transaction spending multiple UTXOs.
func createMultiInputRootTx(utxos []DepositUTXO) *wire.MsgTx {
	rootTx := wire.NewMsgTx(3)

	var totalValue int64
	var pkScript []byte
	for i, utxo := range utxos {
		outPoint := &wire.OutPoint{Hash: utxo.Tx.TxHash(), Index: uint32(utxo.Vout)}
		txIn := wire.NewTxIn(outPoint, nil, nil)
		txIn.Sequence = spark.ZeroSequence
		rootTx.AddTxIn(txIn)
		totalValue += utxo.Tx.TxOut[utxo.Vout].Value
		if i == 0 {
			pkScript = utxo.Tx.TxOut[utxo.Vout].PkScript
		}
	}

	rootTx.AddTxOut(wire.NewTxOut(totalValue, pkScript))
	rootTx.AddTxOut(common.EphemeralAnchorOutput())
	return rootTx
}

// CreateTreeRootWithFinalizeDepositTreeCreationMultiUtxo creates a tree root
// using FinalizeDepositTreeCreation with multiple UTXOs as inputs.
func CreateTreeRootWithFinalizeDepositTreeCreationMultiUtxo(
	ctx context.Context,
	config *TestWalletConfig,
	signingPrivKey keys.Private,
	verifyingKey keys.Public,
	utxos []DepositUTXO,
) (*pb.FinalizeDepositTreeCreationResponse, error) {
	if len(utxos) < 2 {
		return nil, fmt.Errorf("need at least 2 UTXOs for multi-UTXO finalization")
	}
	signingPubKey := signingPrivKey.Public()

	// Create multi-input root tx
	rootTx := createMultiInputRootTx(utxos)
	rootTxInputCount := len(utxos)

	// Build prevOutputs map for multi-input sighash computation
	prevOutputs := make(map[wire.OutPoint]*wire.TxOut)
	for _, utxo := range utxos {
		outPoint := wire.OutPoint{Hash: utxo.Tx.TxHash(), Index: uint32(utxo.Vout)}
		prevOutputs[outPoint] = utxo.Tx.TxOut[utxo.Vout]
	}

	// Prepare signing artifacts for each root tx input
	rootPrepared := make([]*preparedTxSigningArtifacts, rootTxInputCount)
	for i := range rootTxInputCount {
		var err error
		rootPrepared[i], err = prepareTxSigningArtifactsMultiInput(rootTx, i, prevOutputs, signingPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare root tx signing artifacts for input %d: %w", i, err)
		}
	}

	// Serialize primary deposit tx
	var depositBuf bytes.Buffer
	if err := utxos[0].Tx.Serialize(&depositBuf); err != nil {
		return nil, err
	}

	initialRefundSequence, initialDirectSequence := InitialRefundSequences()

	// Create CPFP refund tx (spends root tx output 0)
	cpfpRefundTx, _, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	refundPrepared, err := prepareTxSigningArtifacts(cpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	// Create Direct-From-CPFP Refund Tx
	_, directFromCpfpRefundTx, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	// Get SE commitments: rootTxInputCount for root tx + 1 refund + 1 directFromCpfpRefund
	totalCommitments := uint32(rootTxInputCount + 2)
	commitmentsResp, err := sparkClient.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		Count:       totalCommitments,
		NodeIdCount: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get SE commitments: %w", err)
	}
	if len(commitmentsResp.GetSigningCommitments()) != int(totalCommitments) {
		return nil, fmt.Errorf("got %d commitments, expected %d", len(commitmentsResp.GetSigningCommitments()), totalCommitments)
	}

	// Build FROST signing jobs
	userKeyPackage := CreateUserKeyPackage(signingPrivKey)
	jobIDs := make([]string, rootTxInputCount+2)
	var userSigningJobs []*pbfrost.FrostSigningJob

	for i := range rootTxInputCount {
		jobIDs[i] = uuid.NewString()
		userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
			JobId:           jobIDs[i],
			Message:         rootPrepared[i].sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           rootPrepared[i].nonce,
			UserCommitments: rootPrepared[i].commitment,
			Commitments:     commitmentsResp.GetSigningCommitments()[i].GetSigningNonceCommitments(),
		})
	}

	// Refund job
	refundIdx := rootTxInputCount
	jobIDs[refundIdx] = uuid.NewString()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           jobIDs[refundIdx],
		Message:         refundPrepared.sighash.Serialize(),
		KeyPackage:      userKeyPackage,
		VerifyingKey:    verifyingKey.Serialize(),
		Nonce:           refundPrepared.nonce,
		UserCommitments: refundPrepared.commitment,
		Commitments:     commitmentsResp.GetSigningCommitments()[refundIdx].GetSigningNonceCommitments(),
	})

	// DirectFromCpfpRefund job
	directIdx := rootTxInputCount + 1
	jobIDs[directIdx] = uuid.NewString()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           jobIDs[directIdx],
		Message:         directFromCpfpRefundPrepared.sighash.Serialize(),
		KeyPackage:      userKeyPackage,
		VerifyingKey:    verifyingKey.Serialize(),
		Nonce:           directFromCpfpRefundPrepared.nonce,
		UserCommitments: directFromCpfpRefundPrepared.commitment,
		Commitments:     commitmentsResp.GetSigningCommitments()[directIdx].GetSigningNonceCommitments(),
	})

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sign FROST: %w", err)
	}

	// Extract signatures
	sigs := make([][]byte, rootTxInputCount+2)
	for i, jobID := range jobIDs {
		sig, ok := userSignatures.GetResults()[jobID]
		if !ok || sig == nil {
			returnedResults := slices.Collect(maps.Keys(userSignatures.GetResults()))
			return nil, fmt.Errorf("signature for job %s (index %d) not returned from frost (returned %s)", jobID, i, strings.Join(returnedResults, ","))
		}
		sigs[i] = sig.GetSignatureShare()
	}

	// Build additional UTXO protos and additional input signing data
	additionalUtxoProtos := make([]*pb.UTXO, len(utxos)-1)
	additionalInputs := make([]*pb.InputSigningData, len(utxos)-1)
	for i := 1; i < len(utxos); i++ {
		var addBuf bytes.Buffer
		if err := utxos[i].Tx.Serialize(&addBuf); err != nil {
			return nil, fmt.Errorf("failed to serialize additional deposit tx %d: %w", i, err)
		}
		additionalUtxoProtos[i-1] = &pb.UTXO{
			Vout:    uint32(utxos[i].Vout),
			RawTx:   addBuf.Bytes(),
			Network: config.ProtoNetwork(),
		}
		additionalInputs[i-1] = &pb.InputSigningData{
			SigningNonceCommitment: rootPrepared[i].commitment,
			UserSignature:          sigs[i],
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[i].GetSigningNonceCommitments(),
			},
		}
	}

	// Build the finalize request
	finalizeResp, err := sparkClient.FinalizeDepositTreeCreation(ctx, &pb.FinalizeDepositTreeCreationRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			Vout:    uint32(utxos[0].Vout),
			RawTx:   depositBuf.Bytes(),
			Network: config.ProtoNetwork(),
		},
		AdditionalOnChainUtxos: additionalUtxoProtos,
		RootTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  rootPrepared[0].rawTx,
			SigningNonceCommitment: rootPrepared[0].commitment,
			UserSignature:          sigs[0],
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[0].GetSigningNonceCommitments(),
			},
			AdditionalInputs: additionalInputs,
		},
		RefundTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  refundPrepared.signingJob.GetRawTx(),
			SigningNonceCommitment: refundPrepared.signingJob.GetSigningNonceCommitment(),
			UserSignature:          sigs[refundIdx],
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[refundIdx].GetSigningNonceCommitments(),
			},
		},
		DirectFromCpfpRefundTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  directFromCpfpRefundPrepared.signingJob.GetRawTx(),
			SigningNonceCommitment: directFromCpfpRefundPrepared.signingJob.GetSigningNonceCommitment(),
			UserSignature:          sigs[directIdx],
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[directIdx].GetSigningNonceCommitments(),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	return finalizeResp, nil
}

// CreateTreeRootWithFinalizeDepositTreeCreationWrongOrder creates a multi-input root tx
// with inputs in wrong order (additional UTXO as input 0, primary UTXO as input 1)
// to test that the server rejects mismatched input ordering.
func CreateTreeRootWithFinalizeDepositTreeCreationWrongOrder(
	ctx context.Context,
	config *TestWalletConfig,
	signingPrivKey keys.Private,
	verifyingKey keys.Public,
	primaryDepositTx *wire.MsgTx,
	additionalDepositTx *wire.MsgTx,
) (*pb.FinalizeDepositTreeCreationResponse, error) {
	signingPubKey := signingPrivKey.Public()

	// Build root tx with WRONG order: additional UTXO first, primary second
	rootTx := wire.NewMsgTx(3)
	// Input 0: additional UTXO (wrong - should be primary)
	txIn0 := wire.NewTxIn(&wire.OutPoint{Hash: additionalDepositTx.TxHash(), Index: 0}, nil, nil)
	txIn0.Sequence = spark.ZeroSequence
	rootTx.AddTxIn(txIn0)
	// Input 1: primary UTXO (wrong - should be input 0)
	txIn1 := wire.NewTxIn(&wire.OutPoint{Hash: primaryDepositTx.TxHash(), Index: 0}, nil, nil)
	txIn1.Sequence = spark.ZeroSequence
	rootTx.AddTxIn(txIn1)

	totalValue := primaryDepositTx.TxOut[0].Value + additionalDepositTx.TxOut[0].Value
	pkScript := primaryDepositTx.TxOut[0].PkScript
	rootTx.AddTxOut(wire.NewTxOut(totalValue, pkScript))
	rootTx.AddTxOut(common.EphemeralAnchorOutput())

	// Build prevOutputs map
	prevOutputs := make(map[wire.OutPoint]*wire.TxOut)
	prevOutputs[wire.OutPoint{Hash: primaryDepositTx.TxHash(), Index: 0}] = primaryDepositTx.TxOut[0]
	prevOutputs[wire.OutPoint{Hash: additionalDepositTx.TxHash(), Index: 0}] = additionalDepositTx.TxOut[0]

	// Prepare signing artifacts for each root tx input
	rootPrepared := make([]*preparedTxSigningArtifacts, 2)
	for i := range 2 {
		var err error
		rootPrepared[i], err = prepareTxSigningArtifactsMultiInput(rootTx, i, prevOutputs, signingPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare root tx signing artifacts for input %d: %w", i, err)
		}
	}

	// Serialize primary deposit tx (the one declared as OnChainUtxo)
	var depositBuf bytes.Buffer
	if err := primaryDepositTx.Serialize(&depositBuf); err != nil {
		return nil, err
	}

	initialRefundSequence, initialDirectSequence := InitialRefundSequences()

	cpfpRefundTx, _, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	refundPrepared, err := prepareTxSigningArtifacts(cpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	_, directFromCpfpRefundTx, err := CreateRefundTxs(
		initialRefundSequence,
		initialDirectSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, rootTx.TxOut[0], signingPubKey)
	if err != nil {
		return nil, err
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	totalCommitments := uint32(4) // 2 root inputs + 1 refund + 1 directFromCpfpRefund
	commitmentsResp, err := sparkClient.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		Count:       totalCommitments,
		NodeIdCount: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get SE commitments: %w", err)
	}

	userKeyPackage := CreateUserKeyPackage(signingPrivKey)
	jobIDs := make([]string, 4)
	var userSigningJobs []*pbfrost.FrostSigningJob

	for i := range 2 {
		jobIDs[i] = uuid.NewString()
		userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
			JobId:           jobIDs[i],
			Message:         rootPrepared[i].sighash.Serialize(),
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           rootPrepared[i].nonce,
			UserCommitments: rootPrepared[i].commitment,
			Commitments:     commitmentsResp.GetSigningCommitments()[i].GetSigningNonceCommitments(),
		})
	}

	jobIDs[2] = uuid.NewString()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           jobIDs[2],
		Message:         refundPrepared.sighash.Serialize(),
		KeyPackage:      userKeyPackage,
		VerifyingKey:    verifyingKey.Serialize(),
		Nonce:           refundPrepared.nonce,
		UserCommitments: refundPrepared.commitment,
		Commitments:     commitmentsResp.GetSigningCommitments()[2].GetSigningNonceCommitments(),
	})

	jobIDs[3] = uuid.NewString()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           jobIDs[3],
		Message:         directFromCpfpRefundPrepared.sighash.Serialize(),
		KeyPackage:      userKeyPackage,
		VerifyingKey:    verifyingKey.Serialize(),
		Nonce:           directFromCpfpRefundPrepared.nonce,
		UserCommitments: directFromCpfpRefundPrepared.commitment,
		Commitments:     commitmentsResp.GetSigningCommitments()[3].GetSigningNonceCommitments(),
	})

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sign FROST: %w", err)
	}

	sigs := make([][]byte, 4)
	for i, jobID := range jobIDs {
		sig, ok := userSignatures.GetResults()[jobID]
		if !ok || sig == nil {
			return nil, fmt.Errorf("signature for job %s (index %d) not returned", jobID, i)
		}
		sigs[i] = sig.GetSignatureShare()
	}

	// Serialize additional deposit tx
	var addBuf bytes.Buffer
	if err := additionalDepositTx.Serialize(&addBuf); err != nil {
		return nil, err
	}

	// Build the finalize request with primaryDepositTx as OnChainUtxo
	// and additionalDepositTx as AdditionalOnChainUtxos[0].
	// But the root tx has inputs in reversed order.
	finalizeResp, err := sparkClient.FinalizeDepositTreeCreation(ctx, &pb.FinalizeDepositTreeCreationRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		OnChainUtxo: &pb.UTXO{
			Vout:    0,
			RawTx:   depositBuf.Bytes(),
			Network: config.ProtoNetwork(),
		},
		AdditionalOnChainUtxos: []*pb.UTXO{
			{
				Vout:    0,
				RawTx:   addBuf.Bytes(),
				Network: config.ProtoNetwork(),
			},
		},
		RootTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  rootPrepared[0].rawTx,
			SigningNonceCommitment: rootPrepared[0].commitment,
			UserSignature:          sigs[0],
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[0].GetSigningNonceCommitments(),
			},
			AdditionalInputs: []*pb.InputSigningData{
				{
					SigningNonceCommitment: rootPrepared[1].commitment,
					UserSignature:          sigs[1],
					SigningCommitments: &pb.SigningCommitments{
						SigningCommitments: commitmentsResp.GetSigningCommitments()[1].GetSigningNonceCommitments(),
					},
				},
			},
		},
		RefundTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  refundPrepared.signingJob.GetRawTx(),
			SigningNonceCommitment: refundPrepared.signingJob.GetSigningNonceCommitment(),
			UserSignature:          sigs[2],
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[2].GetSigningNonceCommitments(),
			},
		},
		DirectFromCpfpRefundTxSigningJob: &pb.UserSignedTxSigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  directFromCpfpRefundPrepared.signingJob.GetRawTx(),
			SigningNonceCommitment: directFromCpfpRefundPrepared.signingJob.GetSigningNonceCommitment(),
			UserSignature:          sigs[3],
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: commitmentsResp.GetSigningCommitments()[3].GetSigningNonceCommitments(),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	return finalizeResp, nil
}

type RefundStaticDepositParams struct {
	Network                 btcnetwork.Network
	SpendTx                 *wire.MsgTx
	DepositAddressSecretKey keys.Private
	UserSignature           []byte
	PrevTxOut               *wire.TxOut
}

func GenerateTransferPackage(
	ctx context.Context,
	config *TestWalletConfig,
	receiverIdentityPubkey keys.Public,
	leavesToTransfer []LeafKeyTweak,
	sparkClient pb.SparkServiceClient,
	adaptorPublicKey keys.Public,
) (*pb.TransferPackage, uuid.UUID, error) {
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, uuid.UUID{}, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	keyTweakInputMap, err := PrepareSendTransferKeyTweaks(config, transferID, receiverIdentityPubkey, leavesToTransfer, map[string][]byte{})
	if err != nil {
		return nil, uuid.UUID{}, fmt.Errorf("failed to prepare transfer data: %w", err)
	}
	transferPackage, err := PrepareTransferPackage(
		ctx,
		config,
		sparkClient,
		transferID,
		keyTweakInputMap,
		leavesToTransfer,
		receiverIdentityPubkey,
		adaptorPublicKey,
	)
	if err != nil {
		return nil, uuid.UUID{}, fmt.Errorf("failed to prepare transfer data: %w", err)
	}
	return transferPackage, transferID, nil
}

func RefundStaticDeposit(
	ctx context.Context,
	config *TestWalletConfig,
	params RefundStaticDepositParams,
) (*wire.MsgTx, error) {
	coordinatorConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer coordinatorConn.Close()

	var spendTxBytes bytes.Buffer
	if err := params.SpendTx.Serialize(&spendTxBytes); err != nil {
		return nil, err
	}
	spendTxSighash, err := sighash.FromTx(params.SpendTx, 0, params.PrevTxOut)
	if err != nil {
		return nil, fmt.Errorf("failed to get sighash: %w", err)
	}

	userNonce := frost.GenerateSigningNonce()
	userNonceProto, _ := userNonce.MarshalProto()
	userNonceCommitment := userNonce.SigningCommitment()
	userCommitmentProto, _ := userNonceCommitment.MarshalProto()

	signingJob := &pb.SigningJob{
		RawTx:                  spendTxBytes.Bytes(),
		SigningPublicKey:       params.DepositAddressSecretKey.Public().Serialize(),
		SigningNonceCommitment: userCommitmentProto,
	}

	protoNetwork, err := params.Network.ToProtoNetwork()
	if err != nil {
		return nil, fmt.Errorf("failed to get proto network: %w", err)
	}
	depositTxID, err := hex.DecodeString(params.SpendTx.TxIn[0].PreviousOutPoint.Hash.String())
	if err != nil {
		return nil, fmt.Errorf("failed to decode deposit txid: %w", err)
	}

	// *********************************************************************************
	// Initiate Utxo Swap
	// *********************************************************************************
	sparkClient := pb.NewSparkServiceClient(coordinatorConn)
	swapResponse, err := sparkClient.InitiateStaticDepositUtxoRefund(ctx, &pb.InitiateStaticDepositUtxoRefundRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    depositTxID,
			Vout:    params.SpendTx.TxIn[0].PreviousOutPoint.Index,
			Network: protoNetwork,
		},
		RefundTxSigningJob: signingJob,
		UserSignature:      params.UserSignature,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initiate utxo swap: %w", err)
	}

	// *********************************************************************************
	// Sign the spend tx
	// *********************************************************************************
	frostUserIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  frostUserIdentifier,
		SecretShare: params.DepositAddressSecretKey.Serialize(),
		PublicShares: map[string][]byte{
			frostUserIdentifier: params.DepositAddressSecretKey.Public().Serialize(),
		},
		PublicKey:  swapResponse.GetDepositAddress().GetVerifyingPublicKey(),
		MinSigners: 1,
	}
	operatorCommitments := swapResponse.GetRefundTxSigningResult().GetSigningNonceCommitments()
	userJobID := uuid.NewString()
	userSigningJobs := []*pbfrost.FrostSigningJob{{
		JobId:           userJobID,
		Message:         spendTxSighash.Serialize(),
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    swapResponse.GetDepositAddress().GetVerifyingPublicKey(),
		Nonce:           userNonceProto,
		Commitments:     operatorCommitments,
		UserCommitments: userCommitmentProto,
	}}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sign frost: %w", err)
	}

	signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            spendTxSighash.Serialize(),
		SignatureShares:    swapResponse.GetRefundTxSigningResult().GetSignatureShares(),
		PublicShares:       swapResponse.GetRefundTxSigningResult().GetPublicKeys(),
		VerifyingKey:       swapResponse.GetDepositAddress().GetVerifyingPublicKey(),
		Commitments:        operatorCommitments,
		UserCommitments:    userCommitmentProto,
		UserPublicKey:      params.DepositAddressSecretKey.Public().Serialize(),
		UserSignatureShare: userSignatures.GetResults()[userJobID].GetSignatureShare(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate frost: %w", err)
	}

	// Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.GetSignature())
	if err != nil {
		return nil, err
	}

	pubKey, err := keys.ParsePublicKey(swapResponse.GetDepositAddress().GetVerifyingPublicKey())
	if err != nil {
		return nil, err
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey.ToBTCEC())

	verified := sig.Verify(spendTxSighash[:], taprootKey)
	if !verified {
		return nil, fmt.Errorf("signature verification failed")
	}
	params.SpendTx.TxIn[0].Witness = wire.TxWitness{signatureResult.GetSignature()}

	return params.SpendTx, nil
}

func QueryNodes(
	ctx context.Context,
	config *TestWalletConfig,
	includePending bool,
	limit int64,
	offset int64,
) (map[string]*pb.TreeNode, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	network, err := config.Network.ToProtoNetwork()
	if err != nil {
		return nil, fmt.Errorf("failed to get proto network: %w", err)
	}

	response, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
		Source: &pb.QueryNodesRequest_OwnerIdentityPubkey{
			OwnerIdentityPubkey: config.IdentityPublicKey().Serialize(),
		},
		IncludeParents: includePending,
		Limit:          limit,
		Offset:         offset,
		Network:        network,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query unused deposit addresses at offset %d: %w", offset, err)
	}

	return response.GetNodes(), nil
}

// CreateNewTree creates a new Tree
func CreateNewTree(config *TestWalletConfig, faucet *sparktesting.Faucet, privKey keys.Private, amountSats int64) (*pb.TreeNode, error) {
	coin, err := faucet.Fund()
	if err != nil {
		return nil, fmt.Errorf("failed to fund faucet: %w", err)
	}

	conn, err := sparktesting.DangerousNewGRPCConnectionWithoutVerifyTLS(config.CoordinatorAddress(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to operator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(context.Background(), config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}
	ctx := ContextWithToken(context.Background(), token)

	depositResp, err := GenerateDepositAddress(ctx, config, privKey.Public(), new(uuid.New().String()), false)
	if err != nil {
		return nil, fmt.Errorf("failed to generate deposit address: %w", err)
	}

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.GetDepositAddress().GetAddress(), amountSats)
	if err != nil {
		return nil, fmt.Errorf("failed to create deposit tx: %w", err)
	}
	vout := 0

	verifyingKey, err := keys.ParsePublicKey(depositResp.GetDepositAddress().GetVerifyingKey())
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying key: %w", err)
	}
	resp, err := CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree: %w", err)
	}
	if len(resp.GetNodes()) == 0 {
		return nil, fmt.Errorf("no nodes found after creating tree")
	}

	// Sign, broadcast, mine deposit tx
	signedExitTx, err := sparktesting.SignFaucetCoin(depositTx, coin.TxOut, coin.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to sign deposit tx: %w", err)
	}

	client := sparktesting.GetBitcoinClient()
	_, err = client.SendRawTransaction(signedExitTx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to broadcast deposit tx: %w", err)
	}
	randomKey := keys.GeneratePrivateKey()
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), btcnetwork.Regtest)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random address: %w", err)
	}
	// Mine blocks to reach the confirmation threshold (currently 3 blocks)
	// This ensures the deposit is marked as available by the chain watcher
	_, err = client.GenerateToAddress(4, randomAddress, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to mine deposit confirmation blocks: %w", err)
	}

	// Wait until the deposited leaf is available
	sparkClient := pb.NewSparkServiceClient(conn)
	return WaitForPendingDepositNode(ctx, sparkClient, resp.GetNodes()[0])
}

func WaitForPendingDepositNode(ctx context.Context, sparkClient pb.SparkServiceClient, node *pb.TreeNode) (*pb.TreeNode, error) {
	startTime := time.Now()
	for node.GetStatus() != string(st.TreeNodeStatusAvailable) {
		if time.Since(startTime) >= DepositTimeout {
			return nil, fmt.Errorf("timed out waiting for node to be available")
		}
		time.Sleep(DepositPollInterval)
		nodesResp, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
			Source: &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{node.GetId()}}},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to query nodes: %w", err)
		}
		if len(nodesResp.GetNodes()) != 1 {
			return nil, fmt.Errorf("expected 1 node, got %d", len(nodesResp.GetNodes()))
		}
		node = nodesResp.GetNodes()[node.GetId()]
	}
	return node, nil
}
