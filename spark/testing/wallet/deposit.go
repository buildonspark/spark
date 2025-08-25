package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

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
	pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"
	"github.com/lightsparkdev/spark/so/objects"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	msg := common.ProofOfPossessionMessageHashForDepositAddress(config.IdentityPublicKey().Serialize(), operatorPubKey.Serialize(), []byte(address.Address))
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

		if !sig.Verify(addrHash[:], operator.IdentityPublicKey.ToBTCEC()) {
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
	var rootBuf bytes.Buffer
	if err := rootTx.Serialize(&rootBuf); err != nil {
		return nil, err
	}
	rootNonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, err
	}
	rootNonceProto, err := rootNonce.MarshalProto()
	if err != nil {
		return nil, err
	}
	rootNonceCommitmentProto, err := rootNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, err
	}
	rootTxSighash, err := common.SigHashFromTx(rootTx, 0, depositTx.TxOut[0])
	if err != nil {
		return nil, err
	}
	var depositBuf bytes.Buffer
	err = depositTx.Serialize(&depositBuf)
	if err != nil {
		return nil, err
	}

	// Create refund tx
	cpfpRefundTx, _, err := createRefundTxs(
		spark.InitialSequence(),
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].Value,
		signingPubKey,
		true,
	)
	if err != nil {
		return nil, err
	}
	var refundBuf bytes.Buffer
	err = cpfpRefundTx.Serialize(&refundBuf)
	if err != nil {
		return nil, err
	}
	refundNonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, err
	}
	refundNonceProto, err := refundNonce.MarshalProto()
	if err != nil {
		return nil, err
	}
	refundNonceCommitmentProto, err := refundNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, err
	}
	refundTxSighash, err := common.SigHashFromTx(cpfpRefundTx, 0, rootTx.TxOut[0])
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
		RootTxSigningJob: &pb.SigningJob{
			RawTx:                  rootBuf.Bytes(),
			SigningPublicKey:       signingPubKeyBytes,
			SigningNonceCommitment: rootNonceCommitmentProto,
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:                  refundBuf.Bytes(),
			SigningPublicKey:       signingPubKeyBytes,
			SigningNonceCommitment: refundNonceCommitmentProto,
		},
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
	userSigningJobs := []*pbfrost.FrostSigningJob{
		{
			JobId:           nodeJobID,
			Message:         rootTxSighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    verifyingKey.Serialize(),
			Nonce:           rootNonceProto,
			Commitments:     treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SigningNonceCommitments,
			UserCommitments: rootNonceCommitmentProto,
		},
		{
			JobId:           refundJobID,
			Message:         refundTxSighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    treeResponse.RootNodeSignatureShares.VerifyingKey,
			Nonce:           refundNonceProto,
			Commitments:     treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments: refundNonceCommitmentProto,
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
		Message:            rootTxSighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.NodeTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SigningNonceCommitments,
		UserCommitments:    rootNonceCommitmentProto,
		UserPublicKey:      signingPubKeyBytes,
		UserSignatureShare: userSignatures.Results[nodeJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	refundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            refundTxSighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.RefundTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey.Serialize(),
		Commitments:        treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SigningNonceCommitments,
		UserCommitments:    refundNonceCommitmentProto,
		UserPublicKey:      signingPubKeyBytes,
		UserSignatureShare: userSignatures.Results[refundJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	return sparkClient.FinalizeNodeSignaturesV2(context.Background(), &pb.FinalizeNodeSignaturesRequest{
		Intent: pbcommon.SignatureIntent_CREATION,
		NodeSignatures: []*pb.NodeSignatures{
			{
				NodeId:            treeResponse.RootNodeSignatureShares.NodeId,
				NodeTxSignature:   rootSignature.Signature,
				RefundTxSignature: refundSignature.Signature,
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

	hidingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	bindingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	spendTxNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
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
	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transferID.String(), userIdentityPubKey, leavesToTransfer, map[string][]byte{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}
	transferPackage, err := prepareTransferPackage(ctx, config, sparkClient, transferID, keyTweakInputMap, leavesToTransfer, userIdentityPubKey)
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

	hidingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	bindingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
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

// ClaimStaticDeposit claims a static deposit.
func ClaimStaticDeposit(
	ctx context.Context,
	config *TestWalletConfig,
	network common.Network,
	leavesToTransfer []LeafKeyTweak,
	spendTx *wire.MsgTx,
	depositAddressSecretKey keys.Private,
	userSignature []byte,
	sspSignature []byte,
	userIdentityPubKey keys.Public,
	sspConn *grpc.ClientConn,
	prevTxOut *wire.TxOut,
	receiverIdentityPubKey keys.Public,
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

	hidingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	bindingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	hidingPubBytes := hidingPriv.Public().Serialize()
	bindingPubBytes := bindingPriv.Public().Serialize()
	spendTxNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
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
	sparkSspInternalClient := pbssp.NewSparkSspInternalServiceClient(sspConn)

	creditAmountSats := uint64(0)
	for _, leaf := range leavesToTransfer {
		creditAmountSats += leaf.Leaf.Value
	}
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transferID.String(), userIdentityPubKey, leavesToTransfer, map[string][]byte{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}
	transferPackage, err := prepareTransferPackage(ctx, config, sparkClient, transferID, keyTweakInputMap, leavesToTransfer, userIdentityPubKey)
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
	if receiverIdentityPubKey == (keys.Public{}) {
		receiverIdentityPubKey = userIdentityPubKey
	}
	swapResponse, err := sparkSspInternalClient.InitiateStaticDepositUtxoSwap(ctx, &pbssp.InitiateStaticDepositUtxoSwapRequest{
		OnChainUtxo: &pb.UTXO{
			Txid:    depositTxID,
			Vout:    spendTx.TxIn[0].PreviousOutPoint.Index,
			Network: protoNetwork,
		},
		UserSignature: userSignature,
		SspSignature:  sspSignature,
		Transfer: &pb.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
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
	userIdentityPubkey keys.Public,
	leavesToTransfer []LeafKeyTweak,
	sparkClient pb.SparkServiceClient,
) (*pb.TransferPackage, uuid.UUID, error) {
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, uuid.UUID{}, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transferID.String(), userIdentityPubkey, leavesToTransfer, map[string][]byte{})
	if err != nil {
		return nil, uuid.UUID{}, fmt.Errorf("failed to prepare transfer data: %w", err)
	}
	transferPackage, err := prepareTransferPackage(ctx, config, sparkClient, transferID, keyTweakInputMap, leavesToTransfer, userIdentityPubkey)
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

	hidingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	bindingPriv, err := keys.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
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
