package wallet

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"

	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SwapNodesForPreimage swaps a node for a preimage of a Lightning invoice.
func SwapNodesForPreimage(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	paymentHash []byte,
	invoiceString *string,
	feeSats uint64,
	isInboundPayment bool,
	amountSats uint64,
) (*pb.InitiatePreimageSwapResponse, error) {
	// SSP asks for signing commitment
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	nodeIDs := make([]string, len(leaves))
	for i, leaf := range leaves {
		nodeIDs[i] = leaf.Leaf.Id
	}
	signingCommitments, err := client.GetSigningCommitments(tmpCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodeIDs,
	})
	if err != nil {
		return nil, err
	}

	// SSP signs partial refund tx to receiver
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer signerConn.Close()

	signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobsForUserSignedRefund(leaves, signingCommitments.SigningCommitments, receiverIdentityPubKey)
	if err != nil {
		return nil, err
	}

	signerClient := pbfrost.NewFrostServiceClient(signerConn)
	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	leafSigningJobs, err := prepareLeafSigningJobs(
		leaves,
		refundTxs,
		signingResults.Results,
		userCommitments,
		signingCommitments.SigningCommitments,
	)
	if err != nil {
		return nil, err
	}

	// SSP calls SO to get the preimage
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	bolt11String := ""
	if invoiceString != nil {
		bolt11String = *invoiceString
		bolt11, err := decodepay.Decodepay(bolt11String)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		if bolt11.MSatoshi > 0 {
			amountSats = uint64(bolt11.MSatoshi / 1000)
		}
	}
	reason := pb.InitiatePreimageSwapRequest_REASON_SEND
	if isInboundPayment {
		reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
	}
	response, err := client.InitiatePreimageSwapV2(tmpCtx, &pb.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash,
		Reason:      reason,
		InvoiceAmount: &pb.InvoiceAmount{
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: bolt11String,
			},
			ValueSats: amountSats,
		},
		Transfer: &pb.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			LeavesToSend:              leafSigningJobs,
			ExpiryTime:                timestamppb.New(time.Now().Add(2 * time.Minute)),
		},
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		FeeSats:                   feeSats,
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func ReturnLightningPayment(
	ctx context.Context,
	config *TestWalletConfig,
	paymentHash []byte,
) error {
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return err
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	_, err = client.ReturnLightningPayment(tmpCtx, &pb.ReturnLightningPaymentRequest{
		PaymentHash:           paymentHash,
		UserIdentityPublicKey: config.IdentityPublicKey().Serialize(),
	})
	if err != nil {
		return err
	}
	return nil
}

// SwapNodesForPreimage swaps a node for a preimage of a Lightning invoice.
func SwapNodesForPreimageWithHTLC(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	paymentHash []byte,
	invoiceString *string,
	feeSats uint64,
	isInboundPayment bool,
	amountSats uint64,
) (*pb.InitiatePreimageSwapResponse, error) {
	// SSP asks for signing commitment
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	nodeIDs := make([]string, len(leaves))
	for i, leaf := range leaves {
		nodeIDs[i] = leaf.Leaf.Id
	}
	signingCommitments, err := client.GetSigningCommitments(tmpCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodeIDs,
		Count:   4, // 1 for original refund, 3 for htlc refunds.
	})
	if err != nil {
		return nil, err
	}

	// SSP signs partial refund tx to receiver
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer signerConn.Close()

	originalRefundSigningCommitments := signingCommitments.SigningCommitments[:len(leaves)]
	signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobsForUserSignedRefund(leaves, originalRefundSigningCommitments, receiverIdentityPubKey)
	if err != nil {
		return nil, err
	}

	signerClient := pbfrost.NewFrostServiceClient(signerConn)
	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	leafSigningJobs, err := prepareLeafSigningJobs(
		leaves,
		refundTxs,
		signingResults.Results,
		userCommitments,
		originalRefundSigningCommitments,
	)
	if err != nil {
		return nil, err
	}

	// SSP calls SO to get the preimage
	transferID, err := uuid.NewV7()
	expireTime := time.Now().Add(2 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	bolt11String := ""
	if invoiceString != nil {
		bolt11String = *invoiceString
		bolt11, err := decodepay.Decodepay(bolt11String)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		if bolt11.MSatoshi > 0 {
			amountSats = uint64(bolt11.MSatoshi / 1000)
		}
	}
	reason := pb.InitiatePreimageSwapRequest_REASON_SEND
	var transfer *pb.StartTransferRequest
	if isInboundPayment {
		reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
	} else {
		htlcSigningCommitments := signingCommitments.SigningCommitments[len(leaves):]
		transfer, err = buildLightningHTLCTransfer(ctx, leaves, transferID, config, receiverIdentityPubKey, htlcSigningCommitments, paymentHash, signerConn, expireTime)
		if err != nil {
			return nil, fmt.Errorf("unable to build lightning htlc transfer: %w", err)
		}
	}

	response, err := client.InitiatePreimageSwapV2(tmpCtx, &pb.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash,
		Reason:      reason,
		InvoiceAmount: &pb.InvoiceAmount{
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: bolt11String,
			},
			ValueSats: amountSats,
		},
		Transfer: &pb.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			LeavesToSend:              leafSigningJobs,
			ExpiryTime:                timestamppb.New(expireTime),
		},
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		FeeSats:                   feeSats,
		TransferRequest:           transfer,
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func buildLightningHTLCTransfer(
	ctx context.Context,
	leaves []LeafKeyTweak,
	transferID uuid.UUID,
	config *TestWalletConfig,
	receiverIdentityPubKey keys.Public,
	htlcSigningCommitments []*pb.RequestedSigningCommitments,
	paymentHash []byte,
	signerConn *grpc.ClientConn,
	expireTime time.Time,
) (*pb.StartTransferRequest, error) {
	if len(htlcSigningCommitments) != len(leaves)*3 {
		return nil, fmt.Errorf("number of htlc signing commitments does not match number of leaves")
	}

	cpfpRefundSigningCommitments := htlcSigningCommitments[:len(leaves)]
	directRefundSigningCommitments := htlcSigningCommitments[len(leaves) : len(leaves)*2]
	directFromCpfpRefundSigningCommitments := htlcSigningCommitments[len(leaves)*2:]
	// Build refund htlc transactions
	refundSigningJobs, refundTxs, refundUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundHTLC(
		leaves,
		cpfpRefundSigningCommitments,
		receiverIdentityPubKey,
		config.IdentityPublicKey(),
		PrepareFrostSigningJobsForUserSignedRefundHTLCTypeCPFPRefund,
		config.Network,
		paymentHash,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to prepare frost signing jobs for user signed refund htlc: %w", err)
	}

	signerClient := pbfrost.NewFrostServiceClient(signerConn)
	cpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: refundSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	refundLeafSigningJobs, err := prepareLeafSigningJobs(
		leaves,
		refundTxs,
		cpfpSigningResults.Results,
		refundUserCommitments,
		cpfpRefundSigningCommitments,
	)
	if err != nil {
		return nil, err
	}

	// Build direct htlc transactions from cpfp node tx
	directFromCpfpSigningJobs, directFromCpfpRefundTxs, directFromCpfpUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundHTLC(
		leaves,
		directFromCpfpRefundSigningCommitments,
		receiverIdentityPubKey,
		config.IdentityPublicKey(),
		PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectFromCpfpRefund,
		config.Network,
		paymentHash,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to prepare frost signing jobs for user signed refund htlc: %w", err)
	}

	directFromCpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: directFromCpfpSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	directFromCpfpLeafSigningJobs, err := prepareLeafSigningJobs(
		leaves,
		directFromCpfpRefundTxs,
		directFromCpfpSigningResults.Results,
		directFromCpfpUserCommitments,
		directFromCpfpRefundSigningCommitments,
	)
	if err != nil {
		return nil, err
	}

	// Build direct htlc transactions from direct node tx
	var directRefundLeafSigningJobs []*pb.UserSignedTxSigningJob
	directRefundSigningJobs, directRefundTxs, directRefundUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundHTLC(leaves, directRefundSigningCommitments, receiverIdentityPubKey, config.IdentityPublicKey(), PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectRefund, config.Network, paymentHash)
	if err == nil {
		directRefundSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: directRefundSigningJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, err
		}
		directRefundLeafSigningJobs, err = prepareLeafSigningJobs(
			leaves,
			directRefundTxs,
			directRefundSigningResults.Results,
			directRefundUserCommitments,
			directRefundSigningCommitments,
		)
		if err != nil {
			return nil, err
		}
	}

	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transferID.String(), receiverIdentityPubKey, leaves, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to prepare send transfer key tweaks: %w", err)
	}

	encryptedKeyTweaks := make(map[string][]byte)
	for identifier, keyTweaks := range keyTweakInputMap {
		protoToEncrypt := pb.SendLeafKeyTweaks{
			LeavesToSend: keyTweaks,
		}
		protoToEncryptBinary, err := proto.Marshal(&protoToEncrypt)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal proto to encrypt: %w", err)
		}
		encryptionKeyBytes := config.SigningOperators[identifier].IdentityPublicKey
		encryptionKey, err := eciesgo.NewPublicKeyFromBytes(encryptionKeyBytes.Serialize())
		if err != nil {
			return nil, fmt.Errorf("failed to parse encryption key: %w", err)
		}
		encryptedProto, err := eciesgo.Encrypt(encryptionKey, protoToEncryptBinary)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt proto: %w", err)
		}
		encryptedKeyTweaks[identifier] = encryptedProto
	}

	transferPackage := &pb.TransferPackage{
		LeavesToSend:               refundLeafSigningJobs,
		DirectLeavesToSend:         directRefundLeafSigningJobs,
		DirectFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
		KeyTweakPackage:            encryptedKeyTweaks,
	}

	transferPackageSigningPayload := common.GetTransferPackageSigningPayload(transferID, transferPackage)
	signature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), transferPackageSigningPayload)
	transferPackage.UserSignature = signature.Serialize()

	return &pb.StartTransferRequest{
		TransferId:                transferID.String(),
		OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		TransferPackage:           transferPackage,
		ExpiryTime:                timestamppb.New(expireTime),
	}, nil
}
