package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/keys"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	sparktesting "github.com/lightsparkdev/spark/testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/objects"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	DepositTimeout      = 30 * time.Second
	DepositPollInterval = 100 * time.Millisecond
)

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
	verifyingKey, err := keys.ParsePublicKey(address.VerifyingKey)
	if err != nil {
		return err
	}
	operatorPubKey := verifyingKey.Sub(signingPubKey)
	msg := common.ProofOfPossessionMessageHashForDepositAddress(config.IdentityPublicKey(), operatorPubKey, []byte(address.Address))
	sig, err := schnorr.ParseSignature(address.DepositAddressProof.ProofOfPossessionSignature)
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

	addrHash := sha256.Sum256([]byte(address.Address))
	for _, operator := range config.SigningOperators {
		if operator.Identifier == config.CoordinatorIdentifier && !verifyCoordinatorProof {
			continue
		}

		operatorSig, ok := address.DepositAddressProof.AddressSignatures[operator.Identifier]
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
	if err := validateDepositAddress(config, depositResp.DepositAddress, signingPubkey, false); err != nil {
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
	isStatic := true
	depositResp, err := sparkClient.GenerateDepositAddress(ctx, &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  signingPubKey.Serialize(),
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		Network:           config.ProtoNetwork(),
		IsStatic:          &isStatic,
	})
	if err != nil {
		return nil, err
	}
	if err := validateDepositAddress(config, depositResp.DepositAddress, signingPubKey, false); err != nil {
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
	if err := validateDepositAddress(config, depositResp.DepositAddress, signingPubKey, true); err != nil {
		return nil, err
	}
	return depositResp, nil
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
	network, err := common.ProtoNetworkFromNetwork(config.Network)
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
		allAddresses = append(allAddresses, response.DepositAddresses...)

		// Check if there are more results
		if response.Offset == -1 {
			break // No more results
		}

		offset = response.Offset
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
	network, err := common.ProtoNetworkFromNetwork(config.Network)
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
	for _, address := range addresses.DepositAddresses {
		if err := validateDepositAddress(config, &pb.Address{
			Address:             address.DepositAddress,
			VerifyingKey:        address.VerifyingPublicKey,
			DepositAddressProof: address.ProofOfPossession,
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
	sighash    []byte
	nonce      *pbfrost.SigningNonce
	commitment *pbcommon.SigningCommitment
	signingJob *pb.SigningJob
}

func prepareTxSigningArtifacts(tx *wire.MsgTx, prevTxOut *wire.TxOut, signingPublicKey []byte) (*preparedTxSigningArtifacts, error) {
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}

	nonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, err
	}
	nonceProto, err := nonce.MarshalProto()
	if err != nil {
		return nil, err
	}
	commitmentProto, err := nonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, err
	}

	sighash, err := common.SigHashFromTx(tx, 0, prevTxOut)
	if err != nil {
		return nil, err
	}

	job := &pb.SigningJob{
		RawTx:                  buf.Bytes(),
		SigningPublicKey:       signingPublicKey,
		SigningNonceCommitment: commitmentProto,
	}

	return &preparedTxSigningArtifacts{
		rawTx:      buf.Bytes(),
		sighash:    sighash,
		nonce:      nonceProto,
		commitment: commitmentProto,
		signingJob: job,
	}, nil
}

// CreateTreeRoot creates a tree root for a given deposit transaction.
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
	signingPubKeyBytes := signingPubKey.Serialize()
	// Create root tx
	depositOutPoint := &wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)}
	rootTx := createRootTx(depositOutPoint, depositTx.TxOut[0])
	rootPrepared, err := prepareTxSigningArtifacts(rootTx, depositTx.TxOut[0], signingPubKeyBytes)
	if err != nil {
		return nil, err
	}
	var depositBuf bytes.Buffer
	err = depositTx.Serialize(&depositBuf)
	if err != nil {
		return nil, err
	}

	initialRefundSequence, err := spark.NextSequence(spark.InitialSequence())
	if err != nil {
		return nil, err
	}

	// Create CPFP refund tx
	cpfpRefundTx, _, err := CreateRefundTxs(
		initialRefundSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		false,
	)
	if err != nil {
		return nil, err
	}
	refundPrepared, err := prepareTxSigningArtifacts(cpfpRefundTx, rootTx.TxOut[0], signingPubKeyBytes)
	if err != nil {
		return nil, err
	}

	// Create Direct Root Tx
	directRootTx := wire.NewMsgTx(3)
	directRootTx.AddTxIn(wire.NewTxIn(depositOutPoint, nil, nil))
	directRootAmount := common.MaybeApplyFee(depositTx.TxOut[vout].Value)
	directRootTx.AddTxOut(wire.NewTxOut(directRootAmount, depositTx.TxOut[vout].PkScript))
	directRootPrepared, err := prepareTxSigningArtifacts(directRootTx, depositTx.TxOut[vout], signingPubKeyBytes)
	if err != nil {
		return nil, err
	}

	// Create Direct Refund Tx
	_, directRefundTx, err := CreateRefundTxs(
		initialRefundSequence,
		&wire.OutPoint{Hash: directRootTx.TxHash(), Index: 0},
		directRootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	directRefundPrepared, err := prepareTxSigningArtifacts(directRefundTx, directRootTx.TxOut[0], signingPubKeyBytes)
	if err != nil {
		return nil, err
	}

	// Create Direct-From-CPFP Refund Tx
	_, directFromCpfpRefundTx, err := CreateRefundTxs(
		initialRefundSequence,
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	directFromCpfpRefundPrepared, err := prepareTxSigningArtifacts(directFromCpfpRefundTx, rootTx.TxOut[0], signingPubKeyBytes)
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
		DirectRootTxSigningJob:           directRootPrepared.signingJob,
		DirectRefundTxSigningJob:         directRefundPrepared.signingJob,
		DirectFromCpfpRefundTxSigningJob: directFromCpfpRefundPrepared.signingJob,
	})
	if err != nil {
		return nil, err
	}

	if skipFinalizeSignatures {
		return nil, nil
	}

	rootNodeVerifyingKey, err := keys.ParsePublicKey(treeResponse.RootNodeSignatureShares.VerifyingKey)
	if err != nil {
		return nil, err
	}
	if !rootNodeVerifyingKey.Equals(verifyingKey) {
		return nil, fmt.Errorf("verifying key does not match")
	}

	userKeyPackage := CreateUserKeyPackage(signingPrivKey)

	nodeJobID := uuid.NewString()
	refundJobID := uuid.NewString()
	directRootJobID := uuid.NewString()
	directRefundJobID := uuid.NewString()
	directFromCpfpRefundJobID := uuid.NewString()
	userSigningJobs := []*pbfrost.FrostSigningJob{
		{
			JobId:           nodeJobID,
			Message:         rootPrepared.sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           rootPrepared.nonce,
			Commitments:     treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SigningNonceCommitments,
			UserCommitments: rootPrepared.commitment,
		},
		{
			JobId:           refundJobID,
			Message:         refundPrepared.sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    treeResponse.RootNodeSignatureShares.VerifyingKey,
			Nonce:           refundPrepared.nonce,
			Commitments:     treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments: refundPrepared.commitment,
		},
		{
			JobId:           directRootJobID,
			Message:         directRootPrepared.sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    treeResponse.RootNodeSignatureShares.VerifyingKey,
			Nonce:           directRootPrepared.nonce,
			Commitments:     treeResponse.RootNodeSignatureShares.DirectNodeTxSigningResult.SigningNonceCommitments,
			UserCommitments: directRootPrepared.commitment,
		},
		{
			JobId:           directRefundJobID,
			Message:         directRefundPrepared.sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    treeResponse.RootNodeSignatureShares.VerifyingKey,
			Nonce:           directRefundPrepared.nonce,
			Commitments:     treeResponse.RootNodeSignatureShares.DirectRefundTxSigningResult.SigningNonceCommitments,
			UserCommitments: directRefundPrepared.commitment,
		},
		{
			JobId:           directFromCpfpRefundJobID,
			Message:         directFromCpfpRefundPrepared.sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    treeResponse.RootNodeSignatureShares.VerifyingKey,
			Nonce:           directFromCpfpRefundPrepared.nonce,
			Commitments:     treeResponse.RootNodeSignatureShares.DirectFromCpfpRefundTxSigningResult.SigningNonceCommitments,
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
		Message:            rootPrepared.sighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.NodeTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SigningNonceCommitments,
		UserCommitments:    rootPrepared.commitment,
		UserPublicKey:      signingPubKeyBytes,
		UserSignatureShare: userSignatures.Results[nodeJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	refundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            refundPrepared.sighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.RefundTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SigningNonceCommitments,
		UserCommitments:    refundPrepared.commitment,
		UserPublicKey:      signingPubKeyBytes,
		UserSignatureShare: userSignatures.Results[refundJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	directRootSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            directRootPrepared.sighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.DirectNodeTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.DirectNodeTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.RootNodeSignatureShares.DirectNodeTxSigningResult.SigningNonceCommitments,
		UserCommitments:    directRootPrepared.commitment,
		UserPublicKey:      signingPubKeyBytes,
		UserSignatureShare: userSignatures.Results[directRootJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	directRefundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            directRefundPrepared.sighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.DirectRefundTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.DirectRefundTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.RootNodeSignatureShares.DirectRefundTxSigningResult.SigningNonceCommitments,
		UserCommitments:    directRefundPrepared.commitment,
		UserPublicKey:      signingPubKeyBytes,
		UserSignatureShare: userSignatures.Results[directRefundJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	directFromCpfpRefundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            directFromCpfpRefundPrepared.sighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.DirectFromCpfpRefundTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.DirectFromCpfpRefundTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.RootNodeSignatureShares.DirectFromCpfpRefundTxSigningResult.SigningNonceCommitments,
		UserCommitments:    directFromCpfpRefundPrepared.commitment,
		UserPublicKey:      signingPubKeyBytes,
		UserSignatureShare: userSignatures.Results[directFromCpfpRefundJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	return sparkClient.FinalizeNodeSignaturesV2(ctx, &pb.FinalizeNodeSignaturesRequest{
		Intent: pbcommon.SignatureIntent_CREATION,
		NodeSignatures: []*pb.NodeSignatures{
			{
				NodeId:                          treeResponse.RootNodeSignatureShares.NodeId,
				NodeTxSignature:                 rootSignature.Signature,
				RefundTxSignature:               refundSignature.Signature,
				DirectNodeTxSignature:           directRootSignature.Signature,
				DirectRefundTxSignature:         directRefundSignature.Signature,
				DirectFromCpfpRefundTxSignature: directFromCpfpRefundSignature.Signature,
			},
		},
	})
}

// ClaimStaticDepositLegacy claims a static deposit.
func ClaimStaticDepositLegacy(
	ctx context.Context,
	config *TestWalletConfig,
	network common.Network,
	leavesToTransfer []LeafKeyTweak,
	spendTx *wire.MsgTx,
	requestType pb.UtxoSwapRequestType,
	depositAddressSecretKey keys.Private,
	userSignature []byte,
	sspSignature []byte,
	userIdentityPubKey keys.Public,
	sspConn *grpc.ClientConn,
	prevTxOut *wire.TxOut,
) (*wire.MsgTx, *pb.Transfer, error) {
	var spendTxBytes bytes.Buffer
	err := spendTx.Serialize(&spendTxBytes)
	if err != nil {
		return nil, nil, err
	}
	spendTxSighash, err := common.SigHashFromTx(
		spendTx,
		0,
		prevTxOut,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get sighash: %w", err)
	}

	hidingPriv := keys.GeneratePrivateKey()
	bindingPriv := keys.GeneratePrivateKey()
	spendTxNonceCommitment, err := objects.NewSigningCommitment(bindingPriv.Public().Serialize(), hidingPriv.Public().Serialize())
	if err != nil {
		return nil, nil, err
	}
	spendTxNonceCommitmentProto, err := spendTxNonceCommitment.MarshalProto()
	if err != nil {
		return nil, nil, err
	}

	spendTxSigningJob := &pb.SigningJob{
		RawTx:                  spendTxBytes.Bytes(),
		SigningPublicKey:       depositAddressSecretKey.Public().Serialize(),
		SigningNonceCommitment: spendTxNonceCommitmentProto,
	}

	sparkClient := pb.NewSparkServiceClient(sspConn)

	creditAmountSats := uint64(0)
	for _, leaf := range leavesToTransfer {
		creditAmountSats += leaf.Leaf.Value
	}
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	keyTweakInputMap, err := PrepareSendTransferKeyTweaks(config, transferID.String(), userIdentityPubKey, leavesToTransfer, map[string][]byte{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}
	transferPackage, err := PrepareTransferPackage(ctx, config, sparkClient, transferID, keyTweakInputMap, leavesToTransfer, userIdentityPubKey, keys.Public{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}

	conn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer conn.Close()
	protoNetwork, err := common.ProtoNetworkFromNetwork(network)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get proto network: %w", err)
	}
	depositTxID, err := hex.DecodeString(spendTx.TxIn[0].PreviousOutPoint.Hash.String())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode deposit txid: %w", err)
	}
	swapResponse, err := sparkClient.InitiateUtxoSwap(ctx, &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    depositTxID,
			Vout:    spendTx.TxIn[0].PreviousOutPoint.Index,
			Network: protoNetwork,
		},
		RequestType:   requestType,
		Amount:        &pb.InitiateUtxoSwapRequest_CreditAmountSats{CreditAmountSats: creditAmountSats},
		UserSignature: userSignature,
		SspSignature:  sspSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userIdentityPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(time.Now().Add(2 * time.Minute)),
			TransferPackage:           transferPackage,
		},
		SpendTxSigningJob: spendTxSigningJob,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initiate utxo swap: %w", err)
	}
	// Similar to CreateUserKeyPackage(depositAddressSecretKey.Serialize())
	frostUserIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  frostUserIdentifier,
		SecretShare: depositAddressSecretKey.Serialize(),
		PublicShares: map[string][]byte{
			frostUserIdentifier: depositAddressSecretKey.Public().Serialize(),
		},
		PublicKey:  swapResponse.DepositAddress.VerifyingPublicKey,
		MinSigners: 1,
	}
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	if err != nil {
		return nil, nil, err
	}
	userNonceProto, err := userNonce.MarshalProto()
	if err != nil {
		return nil, nil, err
	}
	userCommitmentProto, err := userNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, nil, err
	}
	operatorCommitments := swapResponse.SpendTxSigningResult.SigningNonceCommitments

	userJobID := uuid.NewString()
	userSigningJobs := []*pbfrost.FrostSigningJob{{
		JobId:           userJobID,
		Message:         spendTxSighash,
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    swapResponse.DepositAddress.VerifyingPublicKey,
		Nonce:           userNonceProto,
		Commitments:     operatorCommitments,
		UserCommitments: userCommitmentProto,
	}}

	frostConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign frost: %w", err)
	}

	signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            spendTxSighash,
		SignatureShares:    swapResponse.SpendTxSigningResult.SignatureShares,
		PublicShares:       swapResponse.SpendTxSigningResult.PublicKeys,
		VerifyingKey:       swapResponse.DepositAddress.VerifyingPublicKey,
		Commitments:        operatorCommitments,
		UserCommitments:    userCommitmentProto,
		UserPublicKey:      depositAddressSecretKey.Public().Serialize(),
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to aggregate frost: %w", err)
	}

	// Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.Signature)
	if err != nil {
		return nil, nil, err
	}

	pubKey, err := btcec.ParsePubKey(swapResponse.DepositAddress.VerifyingPublicKey)
	if err != nil {
		return nil, nil, err
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	verified := sig.Verify(spendTxSighash[:], taprootKey)
	if !verified {
		return nil, nil, fmt.Errorf("signature verification failed")
	}
	spendTx.TxIn[0].Witness = wire.TxWitness{signatureResult.Signature}
	return spendTx, swapResponse.Transfer, nil
}

func RefundStaticDepositLegacy(
	ctx context.Context,
	config *TestWalletConfig,
	network common.Network,
	spendTx *wire.MsgTx,
	depositAddressSecretKey keys.Private,
	userSignature []byte,
	userIdentityPubKey keys.Public,
	prevTxOut *wire.TxOut,
	aliceConn *grpc.ClientConn,
) (*wire.MsgTx, error) {
	var spendTxBytes bytes.Buffer
	err := spendTx.Serialize(&spendTxBytes)
	if err != nil {
		return nil, err
	}
	spendTxSighash, err := common.SigHashFromTx(
		spendTx,
		0,
		prevTxOut,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get sighash: %w", err)
	}

	hidingPriv := keys.GeneratePrivateKey()
	bindingPriv := keys.GeneratePrivateKey()
	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	spendTxNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	if err != nil {
		return nil, err
	}
	spendTxNonceCommitmentProto, err := spendTxNonceCommitment.MarshalProto()
	if err != nil {
		return nil, err
	}

	signingJob := &pb.SigningJob{
		RawTx:                  spendTxBytes.Bytes(),
		SigningPublicKey:       depositAddressSecretKey.Public().Serialize(),
		SigningNonceCommitment: spendTxNonceCommitmentProto,
	}

	protoNetwork, err := common.ProtoNetworkFromNetwork(network)
	if err != nil {
		return nil, fmt.Errorf("failed to get proto network: %w", err)
	}
	depositTxID, err := hex.DecodeString(spendTx.TxIn[0].PreviousOutPoint.Hash.String())
	if err != nil {
		return nil, fmt.Errorf("failed to decode deposit txid: %w", err)
	}

	// *********************************************************************************
	// Initiate Utxo Swap
	// *********************************************************************************
	sparkClient := pb.NewSparkServiceClient(aliceConn)
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	swapResponse, err := sparkClient.InitiateUtxoSwap(ctx, &pb.InitiateUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    depositTxID,
			Vout:    spendTx.TxIn[0].PreviousOutPoint.Index,
			Network: protoNetwork,
		},
		RequestType:   pb.UtxoSwapRequestType_Refund,
		Amount:        &pb.InitiateUtxoSwapRequest_CreditAmountSats{CreditAmountSats: 0},
		UserSignature: userSignature,
		SspSignature:  []byte{},
		Transfer: &pb.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: userIdentityPubKey.Serialize(),
			ExpiryTime:                nil,
			TransferPackage:           nil,
		},
		SpendTxSigningJob: signingJob,
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
		SecretShare: depositAddressSecretKey.Serialize(),
		PublicShares: map[string][]byte{
			frostUserIdentifier: depositAddressSecretKey.Public().Serialize(),
		},
		PublicKey:  swapResponse.DepositAddress.VerifyingPublicKey,
		MinSigners: 1,
	}
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	if err != nil {
		return nil, err
	}
	userNonceProto, err := userNonce.MarshalProto()
	if err != nil {
		return nil, err
	}
	userCommitmentProto, err := userNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, err
	}
	operatorCommitments := swapResponse.SpendTxSigningResult.SigningNonceCommitments

	userJobID := uuid.NewString()
	userSigningJobs := []*pbfrost.FrostSigningJob{{
		JobId:           userJobID,
		Message:         spendTxSighash,
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    swapResponse.DepositAddress.VerifyingPublicKey,
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
		Message:            spendTxSighash,
		SignatureShares:    swapResponse.SpendTxSigningResult.SignatureShares,
		PublicShares:       swapResponse.SpendTxSigningResult.PublicKeys,
		VerifyingKey:       swapResponse.DepositAddress.VerifyingPublicKey,
		Commitments:        operatorCommitments,
		UserCommitments:    userCommitmentProto,
		UserPublicKey:      depositAddressSecretKey.Public().Serialize(),
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate frost: %w", err)
	}

	// Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.Signature)
	if err != nil {
		return nil, err
	}

	pubKey, err := btcec.ParsePubKey(swapResponse.DepositAddress.VerifyingPublicKey)
	if err != nil {
		return nil, err
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	verified := sig.Verify(spendTxSighash[:], taprootKey)
	if !verified {
		return nil, fmt.Errorf("signature verification failed")
	}
	spendTx.TxIn[0].Witness = wire.TxWitness{signatureResult.Signature}

	return spendTx, nil
}

type RefundStaticDepositParams struct {
	Network                 common.Network
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
	keyTweakInputMap, err := PrepareSendTransferKeyTweaks(config, transferID.String(), receiverIdentityPubkey, leavesToTransfer, map[string][]byte{})
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

	if err = params.SpendTx.Serialize(&spendTxBytes); err != nil {
		return nil, err
	}
	spendTxSighash, err := common.SigHashFromTx(params.SpendTx, 0, params.PrevTxOut)
	if err != nil {
		return nil, fmt.Errorf("failed to get sighash: %w", err)
	}

	hidingPriv := keys.GeneratePrivateKey()
	bindingPriv := keys.GeneratePrivateKey()
	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	spendTxNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	if err != nil {
		return nil, err
	}
	spendTxNonceCommitmentProto, err := spendTxNonceCommitment.MarshalProto()
	if err != nil {
		return nil, err
	}

	signingJob := &pb.SigningJob{
		RawTx:                  spendTxBytes.Bytes(),
		SigningPublicKey:       params.DepositAddressSecretKey.Public().Serialize(),
		SigningNonceCommitment: spendTxNonceCommitmentProto,
	}

	protoNetwork, err := common.ProtoNetworkFromNetwork(params.Network)
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
		PublicKey:  swapResponse.DepositAddress.VerifyingPublicKey,
		MinSigners: 1,
	}
	userNonce, err := objects.NewSigningNonce(bindingPriv.Serialize(), hidingPriv.Serialize())
	if err != nil {
		return nil, err
	}
	userNonceProto, err := userNonce.MarshalProto()
	if err != nil {
		return nil, err
	}
	userCommitmentProto, err := userNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, err
	}
	operatorCommitments := swapResponse.RefundTxSigningResult.SigningNonceCommitments

	userJobID := uuid.NewString()
	userSigningJobs := []*pbfrost.FrostSigningJob{{
		JobId:           userJobID,
		Message:         spendTxSighash,
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    swapResponse.DepositAddress.VerifyingPublicKey,
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
		Message:            spendTxSighash,
		SignatureShares:    swapResponse.RefundTxSigningResult.SignatureShares,
		PublicShares:       swapResponse.RefundTxSigningResult.PublicKeys,
		VerifyingKey:       swapResponse.DepositAddress.VerifyingPublicKey,
		Commitments:        operatorCommitments,
		UserCommitments:    userCommitmentProto,
		UserPublicKey:      params.DepositAddressSecretKey.Public().Serialize(),
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate frost: %w", err)
	}

	// Verify signature using go lib.
	sig, err := schnorr.ParseSignature(signatureResult.Signature)
	if err != nil {
		return nil, err
	}

	pubKey, err := keys.ParsePublicKey(swapResponse.DepositAddress.VerifyingPublicKey)
	if err != nil {
		return nil, err
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey.ToBTCEC())

	verified := sig.Verify(spendTxSighash[:], taprootKey)
	if !verified {
		return nil, fmt.Errorf("signature verification failed")
	}
	params.SpendTx.TxIn[0].Witness = wire.TxWitness{signatureResult.Signature}

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
	network, err := common.ProtoNetworkFromNetwork(config.Network)
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

	return response.Nodes, nil
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

	leafID := uuid.New().String()
	depositResp, err := GenerateDepositAddress(ctx, config, privKey.Public(), &leafID, false)
	if err != nil {
		return nil, fmt.Errorf("failed to generate deposit address: %w", err)
	}

	depositTx, err := sparktesting.CreateTestDepositTransaction(coin.OutPoint, depositResp.DepositAddress.Address, amountSats)
	if err != nil {
		return nil, fmt.Errorf("failed to create deposit tx: %w", err)
	}
	vout := 0

	verifyingKey, err := keys.ParsePublicKey(depositResp.DepositAddress.VerifyingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse verifying key: %w", err)
	}
	resp, err := CreateTreeRoot(ctx, config, privKey, verifyingKey, depositTx, vout, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree: %w", err)
	}
	if len(resp.Nodes) == 0 {
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
	randomAddress, err := common.P2TRRawAddressFromPublicKey(randomKey.Public(), common.Regtest)
	if err != nil {
		return nil, fmt.Errorf("failed to generate random address: %w", err)
	}
	_, err = client.GenerateToAddress(1, randomAddress, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to mine deposit tx: %w", err)
	}

	// Wait until the deposited leaf is available
	sparkClient := pb.NewSparkServiceClient(conn)
	return WaitForPendingDepositNode(ctx, sparkClient, resp.Nodes[0])
}

func WaitForPendingDepositNode(ctx context.Context, sparkClient pb.SparkServiceClient, node *pb.TreeNode) (*pb.TreeNode, error) {
	startTime := time.Now()
	for node.Status != string(st.TreeNodeStatusAvailable) {
		if time.Since(startTime) >= DepositTimeout {
			return nil, fmt.Errorf("timed out waiting for node to be available")
		}
		time.Sleep(DepositPollInterval)
		nodesResp, err := sparkClient.QueryNodes(ctx, &pb.QueryNodesRequest{
			Source: &pb.QueryNodesRequest_NodeIds{NodeIds: &pb.TreeNodeIds{NodeIds: []string{node.Id}}},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to query nodes: %w", err)
		}
		if len(nodesResp.Nodes) != 1 {
			return nil, fmt.Errorf("expected 1 node, got %d", len(nodesResp.Nodes))
		}
		node = nodesResp.Nodes[node.Id]
	}
	return node, nil
}
