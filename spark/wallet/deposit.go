package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
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

func validateDepositAddress(config *Config, address *pb.Address, userPubkey []byte) error {
	if address.DepositAddressProof.ProofOfPossessionSignature == nil {
		return fmt.Errorf("proof of possession signature is nil")
	}

	operatorPubkey, err := common.SubtractPublicKeys(address.VerifyingKey, userPubkey)
	if err != nil {
		return err
	}
	msg := common.ProofOfPossessionMessageHashForDepositAddress(config.IdentityPublicKey(), operatorPubkey, []byte(address.Address))
	sig, err := schnorr.ParseSignature(address.DepositAddressProof.ProofOfPossessionSignature)
	if err != nil {
		return err
	}

	pubKey, err := btcec.ParsePubKey(operatorPubkey)
	if err != nil {
		return err
	}
	taprootKey := txscript.ComputeTaprootKeyNoScript(pubKey)

	verified := sig.Verify(msg[:], taprootKey)
	if !verified {
		return fmt.Errorf("signature verification failed")
	}

	if address.DepositAddressProof.AddressSignatures == nil {
		return fmt.Errorf("address signatures is nil")
	}

	addrHash := sha256.Sum256([]byte(address.Address))
	for _, operator := range config.SigningOperators {
		if operator.Identifier == config.CoodinatorIdentifier {
			continue
		}
		operatorPubkey, err := secp256k1.ParsePubKey(operator.IdentityPublicKey)
		if err != nil {
			return err
		}

		operatorSig, ok := address.DepositAddressProof.AddressSignatures[operator.Identifier]
		if !ok {
			return fmt.Errorf("address signature for operator %s is nil", operator.Identifier)
		}

		sig, err := ecdsa.ParseDERSignature(operatorSig)
		if err != nil {
			return err
		}

		if !sig.Verify(addrHash[:], operatorPubkey) {
			return fmt.Errorf("signature verification failed for operator %s", operator.Identifier)
		}
	}
	return nil
}

// GenerateDepositAddress generates a deposit address for a given identity and signing public key.
func GenerateDepositAddress(
	ctx context.Context,
	config *Config,
	signingPubkey []byte,
	// Signing pub key should be generated in a deterministic way from this leaf ID.
	// This will be used as the leaf ID for the leaf node.
	customLeafID *string,
	isStatic bool,
) (*pb.GenerateDepositAddressResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	depositResp, err := sparkClient.GenerateDepositAddress(ctx, &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  signingPubkey,
		IdentityPublicKey: config.IdentityPublicKey(),
		Network:           config.ProtoNetwork(),
		LeafId:            customLeafID,
		IsStatic:          &isStatic,
	})
	if err != nil {
		return nil, err
	}
	if err := validateDepositAddress(config, depositResp.DepositAddress, signingPubkey); err != nil {
		return nil, err
	}
	return depositResp, nil
}

// GenerateStaticDepositAddress generates a static deposit address for a given identity and signing public key.
func GenerateStaticDepositAddress(
	ctx context.Context,
	config *Config,
	signingPubkey []byte,
) (*pb.GenerateDepositAddressResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	isStatic := true
	depositResp, err := sparkClient.GenerateDepositAddress(ctx, &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  signingPubkey,
		IdentityPublicKey: config.IdentityPublicKey(),
		Network:           config.ProtoNetwork(),
		IsStatic:          &isStatic,
	})
	if err != nil {
		return nil, err
	}
	if err := validateDepositAddress(config, depositResp.DepositAddress, signingPubkey); err != nil {
		return nil, err
	}
	return depositResp, nil
}

func QueryUnusedDepositAddresses(
	ctx context.Context,
	config *Config,
) (*pb.QueryUnusedDepositAddressesResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	return sparkClient.QueryUnusedDepositAddresses(ctx, &pb.QueryUnusedDepositAddressesRequest{
		IdentityPublicKey: config.IdentityPublicKey(),
	})
}

// CreateTreeRoot creates a tree root for a given deposit transaction.
func CreateTreeRoot(
	ctx context.Context,
	config *Config,
	signingPrivKey,
	verifyingKey []byte,
	depositTx *wire.MsgTx,
	vout int,
) (*pb.FinalizeNodeSignaturesResponse, error) {
	signingPubkey := secp256k1.PrivKeyFromBytes(signingPrivKey).PubKey()
	signingPubkeyBytes := signingPubkey.SerializeCompressed()
	// Creat root tx
	depositOutPoint := &wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)}
	rootTx := createRootTx(depositOutPoint, depositTx.TxOut[0])
	var rootBuf bytes.Buffer
	err := rootTx.Serialize(&rootBuf)
	if err != nil {
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
		signingPubkey,
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

	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	treeResponse, err := sparkClient.StartDepositTreeCreation(ctx, &pb.StartDepositTreeCreationRequest{
		IdentityPublicKey: config.IdentityPublicKey(),
		OnChainUtxo: &pb.UTXO{
			Vout:    uint32(vout),
			RawTx:   depositBuf.Bytes(),
			Network: config.ProtoNetwork(),
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx:                  rootBuf.Bytes(),
			SigningPublicKey:       signingPubkeyBytes,
			SigningNonceCommitment: rootNonceCommitmentProto,
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:                  refundBuf.Bytes(),
			SigningPublicKey:       signingPubkeyBytes,
			SigningNonceCommitment: refundNonceCommitmentProto,
		},
	})
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(treeResponse.RootNodeSignatureShares.VerifyingKey, verifyingKey) {
		return nil, fmt.Errorf("verifying key does not match")
	}

	userKeyPackage := CreateUserKeyPackage(signingPrivKey)

	userSigningJobs := make([]*pbfrost.FrostSigningJob, 0)
	nodeJobID := uuid.NewString()
	refundJobID := uuid.NewString()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           nodeJobID,
		Message:         rootTxSighash,
		KeyPackage:      userKeyPackage,
		VerifyingKey:    verifyingKey,
		Nonce:           rootNonceProto,
		Commitments:     treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SigningNonceCommitments,
		UserCommitments: rootNonceCommitmentProto,
	})
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           refundJobID,
		Message:         refundTxSighash,
		KeyPackage:      userKeyPackage,
		VerifyingKey:    treeResponse.RootNodeSignatureShares.VerifyingKey,
		Nonce:           refundNonceProto,
		Commitments:     treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SigningNonceCommitments,
		UserCommitments: refundNonceCommitmentProto,
	})

	frostConn, err := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
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
		VerifyingKey:       verifyingKey,
		Commitments:        treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SigningNonceCommitments,
		UserCommitments:    rootNonceCommitmentProto,
		UserPublicKey:      signingPubkeyBytes,
		UserSignatureShare: userSignatures.Results[nodeJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	refundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            refundTxSighash,
		SignatureShares:    treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SignatureShares,
		PublicShares:       treeResponse.RootNodeSignatureShares.RefundTxSigningResult.PublicKeys,
		VerifyingKey:       verifyingKey,
		Commitments:        treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SigningNonceCommitments,
		UserCommitments:    refundNonceCommitmentProto,
		UserPublicKey:      signingPubkeyBytes,
		UserSignatureShare: userSignatures.Results[refundJobID].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	return sparkClient.FinalizeNodeSignatures(context.Background(), &pb.FinalizeNodeSignaturesRequest{
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

// ClaimStaticDeposit claims a static deposit.
func ClaimStaticDeposit(
	ctx context.Context,
	config *Config,
	network common.Network,
	leavesToTransfer []LeafKeyTweak,
	spendTx *wire.MsgTx,
	requestType pb.UtxoSwapRequestType,
	depositAddressSecretKey *secp256k1.PrivateKey,
	userSignature []byte,
	sspSignature []byte,
	userIdentityPubkey *secp256k1.PublicKey,
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
		return nil, nil, fmt.Errorf("failed to get sighash: %v", err)
	}

	hidingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	bindingPriv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, nil, err
	}
	hidingPubBytes := hidingPriv.PubKey().SerializeCompressed()
	bindingPubBytes := bindingPriv.PubKey().SerializeCompressed()
	spendTxNonceCommitment, err := objects.NewSigningCommitment(bindingPubBytes, hidingPubBytes)
	if err != nil {
		return nil, nil, err
	}
	spendTxNonceCommitmentProto, err := spendTxNonceCommitment.MarshalProto()
	if err != nil {
		return nil, nil, err
	}

	signingJob := &pb.SigningJob{
		RawTx:                  spendTxBytes.Bytes(),
		SigningPublicKey:       depositAddressSecretKey.PubKey().SerializeCompressed(),
		SigningNonceCommitment: spendTxNonceCommitmentProto,
	}

	sparkClient := pb.NewSparkServiceClient(sspConn)

	creditAmountSats := uint64(0)
	for _, leaf := range leavesToTransfer {
		creditAmountSats += leaf.Leaf.Value
	}
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate transfer id: %v", err)
	}

	nodeIDs := make([]string, len(leavesToTransfer))
	for i, leaf := range leavesToTransfer {
		nodeIDs[i] = leaf.Leaf.Id
	}
	signingCommitments, err := sparkClient.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodeIDs,
	})
	if err != nil {
		return nil, nil, err
	}
	signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobsForUserSignedRefund(
		leavesToTransfer,
		signingCommitments.SigningCommitments,
		userIdentityPubkey,
	)
	if err != nil {
		return nil, nil, err
	}
	// signerClient := pbfrost.NewFrostServiceClient(sspConn)
	conn, err := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to frost signer: %v", err)
	}
	defer conn.Close()
	signerClient := pbfrost.NewFrostServiceClient(conn)
	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign frost: %v", err)
	}
	leafSigningJobs, err := prepareLeafSigningJobs(
		leavesToTransfer,
		refundTxs,
		signingResults.Results,
		userCommitments,
		signingCommitments.SigningCommitments,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to prepare leaf signing jobs: %v", err)
	}
	protoNetwork, err := common.ProtoNetworkFromNetwork(network)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get proto network: %v", err)
	}
	depositTxID, err := hex.DecodeString(spendTx.TxIn[0].PreviousOutPoint.Hash.String())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode deposit txid: %v", err)
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
		Transfer: &pb.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey(),
			ReceiverIdentityPublicKey: userIdentityPubkey.SerializeCompressed(),
			LeavesToSend:              leafSigningJobs,
			ExpiryTime:                timestamppb.New(time.Now().Add(2 * time.Minute)),
		},
		SpendTxSigningJob: signingJob,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initiate utxo swap: %v", err)
	}
	// Similar to CreateUserKeyPackage(depositAddressSecretKey.Serialize())
	frostUserIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  frostUserIdentifier,
		SecretShare: depositAddressSecretKey.Serialize(),
		PublicShares: map[string][]byte{
			frostUserIdentifier: depositAddressSecretKey.PubKey().SerializeCompressed(),
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

	userSigningJobs := make([]*pbfrost.FrostSigningJob, 0)
	userJobID := uuid.NewString()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:           userJobID,
		Message:         spendTxSighash,
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    swapResponse.DepositAddress.VerifyingPublicKey,
		Nonce:           userNonceProto,
		Commitments:     operatorCommitments,
		UserCommitments: userCommitmentProto,
	})

	frostConn, err := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to frost signer: %v", err)
	}
	defer frostConn.Close()

	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign frost: %v", err)
	}

	signatureResult, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            spendTxSighash,
		SignatureShares:    swapResponse.SpendTxSigningResult.SignatureShares,
		PublicShares:       swapResponse.SpendTxSigningResult.PublicKeys,
		VerifyingKey:       swapResponse.DepositAddress.VerifyingPublicKey,
		Commitments:        operatorCommitments,
		UserCommitments:    userCommitmentProto,
		UserPublicKey:      depositAddressSecretKey.PubKey().SerializeCompressed(),
		UserSignatureShare: userSignatures.Results[userJobID].SignatureShare,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to aggregate frost: %v", err)
	}

	// Step 9: Verify signature using go lib.
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

	transferToAliceKeysTweaked, err := SendTransferTweakKey(context.Background(), config, swapResponse.Transfer, leavesToTransfer[:], nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to send transfer tweak key: %v", err)
	}
	if transferToAliceKeysTweaked.Status != pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED {
		return nil, nil, fmt.Errorf("transfer to alice keys tweaked status is not TRANSFER_STATUS_SENDER_KEY_TWEAKED")
	}

	return spendTx, transferToAliceKeysTweaked, nil
}
