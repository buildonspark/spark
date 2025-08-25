package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/objects"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LeafKeyTweak is a struct to hold leaf key to tweak.
type LeafKeyTweak struct {
	Leaf              *pb.TreeNode
	SigningPrivKey    keys.Private
	NewSigningPrivKey keys.Private
}

// SendTransfer initiates a transfer from sender.
func SendTransfer(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	expiryTime time.Time,
) (*pb.Transfer, error) {
	transfer, refundSignatureMap, _, err := SendTransferSignRefund(ctx, config, leaves, receiverIdentityPubKey, expiryTime)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund: %w", err)
	}
	transfer, err = SendTransferTweakKey(ctx, config, transfer, leaves, refundSignatureMap)
	if err != nil {
		return nil, fmt.Errorf("failed to tweak key: %w", err)
	}
	return transfer, nil
}

func CreateTransferPackage(
	ctx context.Context,
	transferID uuid.UUID,
	config *TestWalletConfig,
	client pb.SparkServiceClient,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
) (*pb.TransferPackage, error) {
	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transferID.String(), receiverIdentityPubKey, leaves, map[string][]byte{})
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}

	return prepareTransferPackage(ctx, config, client, transferID, keyTweakInputMap, leaves, receiverIdentityPubKey)
}

func SendTransferWithKeyTweaks(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubkey keys.Public,
	expiryTime time.Time,
) (*pb.Transfer, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(sparkConn)
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}

	transferPackage, err := CreateTransferPackage(authCtx, transferID, config, client, leaves, receiverIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}

	resp, err := client.StartTransferV2(authCtx, &pb.StartTransferRequest{
		TransferId:                transferID.String(),
		OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
		ReceiverIdentityPublicKey: receiverIdentityPubkey.Serialize(),
		ExpiryTime:                timestamppb.New(expiryTime),
		TransferPackage:           transferPackage,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start transfer: %w", err)
	}

	return resp.Transfer, nil
}

func prepareTransferPackage(
	ctx context.Context,
	config *TestWalletConfig,
	client pb.SparkServiceClient,
	transferID uuid.UUID,
	keyTweakInputMap map[string][]*pb.SendLeafKeyTweak,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
) (*pb.TransferPackage, error) {
	// Fetch signing commitments.
	nodes := make([]string, len(leaves))
	for i, leaf := range leaves {
		nodes[i] = leaf.Leaf.Id
	}
	signingCommitments, err := client.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get signing commitments: %w", err)
	}

	// Sign user refund.
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, err
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

	// Encrypt key tweaks.
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
		LeavesToSend:    leafSigningJobs,
		KeyTweakPackage: encryptedKeyTweaks,
	}

	transferPackageSigningPayload := common.GetTransferPackageSigningPayload(transferID, transferPackage)
	signature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), transferPackageSigningPayload)
	transferPackage.UserSignature = signature.Serialize()

	return transferPackage, nil
}

func DeliverTransferPackage(
	ctx context.Context,
	config *TestWalletConfig,
	transfer *pb.Transfer,
	leaves []LeafKeyTweak,
	refundSignatureMap map[string][]byte,
) (*pb.Transfer, error) {
	transferReceiverPubKey, err := keys.ParsePublicKey(transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse receiver identity public key: %w", err)
	}
	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transfer.Id, transferReceiverPubKey, leaves, refundSignatureMap)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare key tweaks: %w", err)
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(sparkConn)

	transferUUID, err := uuid.Parse(transfer.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to parse transfer id %s: %w", transfer.Id, err)
	}

	transferPackage, err := prepareTransferPackage(authCtx, config, client, transferUUID, keyTweakInputMap, leaves, transferReceiverPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}

	resp, err := client.FinalizeTransferWithTransferPackage(authCtx, &pb.FinalizeTransferWithTransferPackageRequest{
		TransferId:             transfer.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		TransferPackage:        transferPackage,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to finalize transfer: %w", err)
	}
	return resp.Transfer, nil
}

// Deprecated: use DeliverTransferPackage instead.
func SendTransferTweakKey(
	ctx context.Context,
	config *TestWalletConfig,
	transfer *pb.Transfer,
	leaves []LeafKeyTweak,
	refundSignatureMap map[string][]byte,
) (*pb.Transfer, error) {
	transferReceiverPubKey, err := keys.ParsePublicKey(transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse receiver identity public key: %w", err)
	}
	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transfer.Id, transferReceiverPubKey, leaves, refundSignatureMap)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}

	var updatedTransfer *pb.Transfer
	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))
	for identifier, operator := range config.SigningOperators {
		wg.Add(1)
		go func(identifier string, operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				results <- err
				return
			}
			defer sparkConn.Close()
			sparkClient := pb.NewSparkServiceClient(sparkConn)
			token, err := AuthenticateWithConnection(ctx, config, sparkConn)
			if err != nil {
				results <- fmt.Errorf("failed to authenticate with server: %w", err)
				return
			}
			tmpCtx := ContextWithToken(ctx, token)
			transferResp, err := sparkClient.FinalizeTransfer(tmpCtx, &pb.FinalizeTransferRequest{
				TransferId:             transfer.Id,
				OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
				LeavesToSend:           keyTweakInputMap[identifier],
			})
			if err != nil {
				results <- fmt.Errorf("failed to call SendTransfer: %w", err)
				return
			}
			if updatedTransfer == nil {
				updatedTransfer = transferResp.Transfer
			} else {
				if !compareTransfers(updatedTransfer, transferResp.Transfer) {
					results <- fmt.Errorf("inconsistent transfer response from operators")
					return
				}
			}
		}(identifier, operator)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result != nil {
			return nil, result
		}
	}
	return updatedTransfer, nil
}

func SendTransferSignRefund(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, map[string]*LeafRefundSigningData, error) {
	senderTransfer, senderRefundSignatureMap, leafDataMap, _, err := sendTransferSignRefund(ctx, config, leaves, receiverIdentityPubKey, expiryTime, false, keys.Public{})
	return senderTransfer, senderRefundSignatureMap, leafDataMap, err
}

func StartSwapSignRefund(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, map[string]*LeafRefundSigningData, error) {
	senderTransfer, senderRefundSignatureMap, leafDataMap, _, err := sendTransferSignRefund(ctx, config, leaves, receiverIdentityPubKey, expiryTime, true, keys.Public{})
	return senderTransfer, senderRefundSignatureMap, leafDataMap, err
}

func CounterSwapSignRefund(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	expiryTime time.Time,
	adaptorPublicKey keys.Public,
) (*pb.Transfer, map[string][]byte, map[string]*LeafRefundSigningData, []*pb.LeafRefundTxSigningResult, error) {
	return sendTransferSignRefund(ctx, config, leaves, receiverIdentityPubKey, expiryTime, true, adaptorPublicKey)
}

func sendTransferSignRefund(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	expiryTime time.Time,
	forSwap bool,
	adaptorPublicKey keys.Public,
) (*pb.Transfer, map[string][]byte, map[string]*LeafRefundSigningData, []*pb.LeafRefundTxSigningResult, error) {
	transferID, err := uuid.NewRandom()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}

	leafDataMap := make(map[string]*LeafRefundSigningData)
	for _, leafKey := range leaves {
		nonce, _ := objects.RandomSigningNonce()
		tx, _ := common.TxFromRawTxBytes(leafKey.Leaf.NodeTx)
		leafDataMap[leafKey.Leaf.Id] = &LeafRefundSigningData{
			SigningPrivKey:  leafKey.SigningPrivKey,
			ReceivingPubKey: receiverIdentityPubKey,
			Nonce:           nonce,
			Tx:              tx,
			Vout:            int(leafKey.Leaf.Vout),
		}
	}

	signingJobs, err := prepareRefundSoSigningJobs(leaves, leafDataMap)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to prepare signing jobs for sending transfer: %w", err)
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	startTransferRequest := &pb.StartTransferRequest{
		TransferId:                transferID.String(),
		LeavesToSend:              signingJobs,
		OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		ExpiryTime:                timestamppb.New(expiryTime),
	}
	// Whether it's a swap or normal transfer, we're doing the same thing and getting
	// back the same results.
	var transfer *pb.Transfer
	var signingResults []*pb.LeafRefundTxSigningResult
	if adaptorPublicKey != (keys.Public{}) {
		swapID, err := uuid.NewV7()
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to generate swap id: %w", err)
		}
		response, err := sparkClient.CounterLeafSwapV2(tmpCtx, &pb.CounterLeafSwapRequest{
			Transfer:         startTransferRequest,
			SwapId:           swapID.String(),
			AdaptorPublicKey: adaptorPublicKey.Serialize(),
		})
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to start transfer: %w", err)
		}
		transfer = response.Transfer
		signingResults = response.SigningResults
	} else if forSwap {
		response, err := sparkClient.StartLeafSwapV2(tmpCtx, startTransferRequest)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to start transfer: %w", err)
		}
		transfer = response.Transfer
		signingResults = response.SigningResults
	} else {
		response, err := sparkClient.StartTransferV2(tmpCtx, startTransferRequest)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("failed to start transfer: %w", err)
		}
		transfer = response.Transfer
		signingResults = response.SigningResults
	}

	signatures, err := signRefunds(config, leafDataMap, signingResults, adaptorPublicKey)
	if err != nil {
		return transfer, nil, nil, nil, fmt.Errorf("failed to sign refunds for send: %w", err)
	}
	signatureMap := make(map[string][]byte)
	for _, signature := range signatures {
		signatureMap[signature.NodeId] = signature.RefundTxSignature
	}
	return transfer, signatureMap, leafDataMap, signingResults, nil
}

func compareTransfers(transfer1, transfer2 *pb.Transfer) bool {
	return transfer1.Id == transfer2.Id &&
		bytes.Equal(transfer1.ReceiverIdentityPublicKey, transfer2.ReceiverIdentityPublicKey) &&
		transfer1.Status == transfer2.Status &&
		transfer1.TotalValue == transfer2.TotalValue &&
		transfer1.ExpiryTime.AsTime().Equal(transfer2.ExpiryTime.AsTime()) &&
		len(transfer1.Leaves) == len(transfer2.Leaves)
}

func prepareSendTransferKeyTweaks(config *TestWalletConfig, transferID string, receiverIdentityPubkey keys.Public, leaves []LeafKeyTweak, refundSignatureMap map[string][]byte) (map[string][]*pb.SendLeafKeyTweak, error) {
	receiverEciesPubKey, err := eciesgo.NewPublicKeyFromBytes(receiverIdentityPubkey.Serialize())
	if err != nil {
		return nil, fmt.Errorf("failed to parse receiver public key: %w", err)
	}

	leavesTweaksMap := make(map[string][]*pb.SendLeafKeyTweak)
	for _, leaf := range leaves {
		leafTweaksMap, err := prepareSingleSendTransferKeyTweak(config, transferID, leaf, receiverEciesPubKey, refundSignatureMap[leaf.Leaf.Id])
		if err != nil {
			return nil, fmt.Errorf("failed to prepare single leaf transfer: %w", err)
		}
		for identifier, leafTweak := range leafTweaksMap {
			leavesTweaksMap[identifier] = append(leavesTweaksMap[identifier], leafTweak)
		}
	}
	return leavesTweaksMap, nil
}

func prepareSingleSendTransferKeyTweak(config *TestWalletConfig, transferID string, leaf LeafKeyTweak, receiverEciesPubKey *eciesgo.PublicKey, refundSignature []byte) (map[string]*pb.SendLeafKeyTweak, error) {
	privKeyTweak := leaf.SigningPrivKey.Sub(leaf.NewSigningPrivKey)

	// Calculate secret tweak shares
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak.Serialize()),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to split private key tweak: %w", err)
	}

	// Calculate pubkey shares tweak
	pubkeySharesTweak := make(map[string][]byte)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		privKey, err := keys.PrivateKeyFromBigInt(share.Share)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key for operator %d: %w", operator.ID, err)
		}
		pubkeySharesTweak[identifier] = privKey.Public().Serialize()
	}

	secretCipher, err := eciesgo.Encrypt(receiverEciesPubKey, leaf.NewSigningPrivKey.Serialize())
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt new signing private key: %w", err)
	}

	// Generate signature over Sha256(leaf_id||transfer_id||secret_cipher)
	payload := append(append([]byte(leaf.Leaf.Id), []byte(transferID)...), secretCipher...)
	payloadHash := sha256.Sum256(payload)
	signature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), payloadHash[:])

	leafTweaksMap := make(map[string]*pb.SendLeafKeyTweak)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		leafTweaksMap[identifier] = &pb.SendLeafKeyTweak{
			LeafId: leaf.Leaf.Id,
			SecretShareTweak: &pb.SecretShare{
				SecretShare: share.Share.Bytes(),
				Proofs:      share.Proofs,
			},
			PubkeySharesTweak: pubkeySharesTweak,
			SecretCipher:      secretCipher,
			Signature:         signature.Serialize(),
			RefundSignature:   refundSignature,
		}
	}
	return leafTweaksMap, nil
}

func findShare(shares []*secretsharing.VerifiableSecretShare, operatorID uint64) *secretsharing.VerifiableSecretShare {
	targetShareIndex := big.NewInt(int64(operatorID + 1))
	for _, s := range shares {
		if s.Index.Cmp(targetShareIndex) == 0 {
			return s
		}
	}
	return nil
}

// QueryPendingTransfers queries pending transfers to claim.
func QueryPendingTransfers(
	ctx context.Context,
	config *TestWalletConfig,
) (*pb.QueryTransfersResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	network, err := common.ProtoNetworkFromNetwork(config.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to convert network to proto network: %w", err)
	}
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	return sparkClient.QueryPendingTransfers(ctx, &pb.TransferFilter{
		Participant: &pb.TransferFilter_ReceiverIdentityPublicKey{
			ReceiverIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		},
		Network: network,
	})
}

func QueryPendingTransfersBySender(
	ctx context.Context,
	config *TestWalletConfig,
) (*pb.QueryTransfersResponse, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	network, err := common.ProtoNetworkFromNetwork(config.Network)
	if err != nil {
		return nil, fmt.Errorf("failed to convert network to proto network: %w", err)
	}
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	return sparkClient.QueryPendingTransfers(ctx, &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderIdentityPublicKey{
			SenderIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		},
		Network: network,
	})
}

// VerifyPendingTransfer verifies signature and decrypt secret cipher for all leaves in the transfer.
func VerifyPendingTransfer(_ context.Context, config *TestWalletConfig, transfer *pb.Transfer) (map[string][]byte, error) {
	leafPrivKeyMap := make(map[string][]byte)
	senderPubkey, err := secp256k1.ParsePubKey(transfer.SenderIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sender public key: %w", err)
	}

	receiverEciesPrivKey := eciesgo.NewPrivateKeyFromBytes(config.IdentityPrivateKey.Serialize())
	for _, leaf := range transfer.Leaves {
		// Verify signature
		signature, err := ecdsa.ParseDERSignature(leaf.Signature)
		if err != nil {
			if len(leaf.Signature) == 64 {
				r := secp256k1.ModNScalar{}
				r.SetByteSlice(leaf.Signature[:32])
				s := secp256k1.ModNScalar{}
				s.SetByteSlice(leaf.Signature[32:64])
				signature = ecdsa.NewSignature(&r, &s)
			} else {
				return nil, fmt.Errorf("failed to parse signature: %w", err)
			}
		}
		payload := append(append([]byte(leaf.Leaf.Id), []byte(transfer.Id)...), leaf.SecretCipher...)
		payloadHash := sha256.Sum256(payload)
		if !signature.Verify(payloadHash[:], senderPubkey) {
			return nil, fmt.Errorf("failed to verify signature: %w", err)
		}

		// Decrypt secret cipher
		leafSecret, err := eciesgo.Decrypt(receiverEciesPrivKey, leaf.SecretCipher)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt secret cipher: %w", err)
		}
		leafPrivKeyMap[leaf.Leaf.Id] = leafSecret

	}
	return leafPrivKeyMap, nil
}

// ClaimTransfer claims a pending transfer.
func ClaimTransfer(
	ctx context.Context,
	transfer *pb.Transfer,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
) ([]*pb.TreeNode, error) {
	proofMap := make(map[string][][]byte)
	if transfer.Status == pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED {
		var err error
		proofMap, err = ClaimTransferTweakKeys(ctx, transfer, config, leaves)
		if err != nil {
			return nil, fmt.Errorf("failed to tweak keys when claiming leaves: %w", err)
		}
	}

	signatures, err := ClaimTransferSignRefunds(ctx, transfer, config, leaves, proofMap)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refunds when claiming leaves: %w", err)
	}

	return FinalizeTransfer(ctx, config, signatures)
}

func ClaimTransferWithoutFinalizeSignatures(
	ctx context.Context,
	transfer *pb.Transfer,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
) ([]*pb.NodeSignatures, error) {
	proofMap := make(map[string][][]byte)
	if transfer.Status == pb.TransferStatus_TRANSFER_STATUS_SENDER_KEY_TWEAKED {
		var err error
		proofMap, err = ClaimTransferTweakKeys(ctx, transfer, config, leaves)
		if err != nil {
			return nil, fmt.Errorf("failed to tweak keys when claiming leaves: %w", err)
		}
	}

	signatures, err := ClaimTransferSignRefunds(ctx, transfer, config, leaves, proofMap)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refunds when claiming leaves: %w", err)
	}
	return signatures, nil
}

func ClaimTransferTweakKeys(
	ctx context.Context,
	transfer *pb.Transfer,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
) (map[string][][]byte, error) {
	leavesTweaksMap, proofMap, err := prepareClaimLeavesKeyTweaks(config, leaves)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %w", err)
	}

	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))

	for identifier, operator := range config.SigningOperators {
		wg.Add(1)
		go func(identifier string, operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := operator.NewOperatorGRPCConnection()
			if err != nil {
				results <- err
				return
			}
			defer sparkConn.Close()
			token, err := AuthenticateWithConnection(ctx, config, sparkConn)
			if err != nil {
				results <- err
				return
			}
			tmpCtx := ContextWithToken(ctx, token)
			sparkClient := pb.NewSparkServiceClient(sparkConn)
			_, err = sparkClient.ClaimTransferTweakKeys(tmpCtx, &pb.ClaimTransferTweakKeysRequest{
				TransferId:             transfer.Id,
				OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
				LeavesToReceive:        leavesTweaksMap[identifier],
			})
			if err != nil {
				results <- fmt.Errorf("failed to call ClaimTransferTweakKeys: %w", err)
			}
		}(identifier, operator)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result != nil {
			return nil, result
		}
	}
	return proofMap, nil
}

func prepareClaimLeavesKeyTweaks(config *TestWalletConfig, leaves []LeafKeyTweak) (map[string][]*pb.ClaimLeafKeyTweak, map[string][][]byte, error) {
	leavesTweaksMap := make(map[string][]*pb.ClaimLeafKeyTweak)
	proofMap := make(map[string][][]byte)
	for _, leaf := range leaves {
		leafTweaks, proof, err := prepareClaimLeafKeyTweaks(config, leaf)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to prepare single leaf transfer: %w", err)
		}
		proofMap[leaf.Leaf.Id] = proof
		for identifier, leafTweak := range leafTweaks {
			leavesTweaksMap[identifier] = append(leavesTweaksMap[identifier], leafTweak)
		}
	}
	return leavesTweaksMap, proofMap, nil
}

func prepareClaimLeafKeyTweaks(config *TestWalletConfig, leaf LeafKeyTweak) (map[string]*pb.ClaimLeafKeyTweak, [][]byte, error) {
	privKeyTweak := leaf.SigningPrivKey.Sub(leaf.NewSigningPrivKey)

	// Calculate secret tweak shares
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak.Serialize()),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to split private key tweak: %w", err)
	}

	// Calculate pubkey shares tweak
	pubkeySharesTweak := make(map[string][]byte)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		var shareScalar secp256k1.ModNScalar
		shareScalar.SetByteSlice(share.Share.Bytes())
		pubkeyTweak := secp256k1.NewPrivateKey(&shareScalar).PubKey()
		pubkeySharesTweak[identifier] = pubkeyTweak.SerializeCompressed()
	}

	leafTweaksMap := make(map[string]*pb.ClaimLeafKeyTweak)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		leafTweaksMap[identifier] = &pb.ClaimLeafKeyTweak{
			LeafId: leaf.Leaf.Id,
			SecretShareTweak: &pb.SecretShare{
				SecretShare: share.Share.Bytes(),
				Proofs:      share.Proofs,
			},
			PubkeySharesTweak: pubkeySharesTweak,
		}
	}
	return leafTweaksMap, shares[0].Proofs, nil
}

type LeafRefundSigningData struct {
	SigningPrivKey  keys.Private
	ReceivingPubKey keys.Public
	Tx              *wire.MsgTx
	RefundTx        *wire.MsgTx
	Nonce           *objects.SigningNonce
	Vout            int
}

func ClaimTransferSignRefunds(
	ctx context.Context,
	transfer *pb.Transfer,
	config *TestWalletConfig,
	leafKeys []LeafKeyTweak,
	proofMap map[string][][]byte,
) ([]*pb.NodeSignatures, error) {
	leafDataMap := make(map[string]*LeafRefundSigningData)
	for _, leafKey := range leafKeys {
		nonce, _ := objects.RandomSigningNonce()
		tx, _ := common.TxFromRawTxBytes(leafKey.Leaf.NodeTx)
		leafDataMap[leafKey.Leaf.Id] = &LeafRefundSigningData{
			SigningPrivKey:  leafKey.NewSigningPrivKey,
			ReceivingPubKey: leafKey.NewSigningPrivKey.Public(),
			Nonce:           nonce,
			Tx:              tx,
			Vout:            int(leafKey.Leaf.Vout),
		}
	}

	signingJobs, err := prepareRefundSoSigningJobs(leafKeys, leafDataMap)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare signing jobs for claiming transfer: %w", err)
	}
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	secretProofMap := make(map[string]*pb.SecretProof)
	for leafID, proof := range proofMap {
		secretProofMap[leafID] = &pb.SecretProof{
			Proofs: proof,
		}
	}
	response, err := sparkClient.ClaimTransferSignRefundsV2(ctx, &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to call ClaimTransferSignRefunds: %w", err)
	}

	return signRefunds(config, leafDataMap, response.SigningResults, keys.Public{})
}

func FinalizeTransfer(
	ctx context.Context,
	config *TestWalletConfig,
	signatures []*pb.NodeSignatures,
) ([]*pb.TreeNode, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.FinalizeNodeSignaturesV2(ctx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_TRANSFER,
		NodeSignatures: signatures,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to call FinalizeNodeSignatures: %w", err)
	}
	return response.Nodes, nil
}

func signRefunds(
	config *TestWalletConfig,
	leafDataMap map[string]*LeafRefundSigningData,
	operatorSigningResults []*pb.LeafRefundTxSigningResult,
	adaptorPublicKey keys.Public,
) ([]*pb.NodeSignatures, error) {
	var adaptorPublicKeyBytes []byte
	if adaptorPublicKey != (keys.Public{}) {
		adaptorPublicKeyBytes = adaptorPublicKey.Serialize()
	}

	var userSigningJobs []*pbfrost.FrostSigningJob
	jobToAggregateRequestMap := make(map[string]*pbfrost.AggregateFrostRequest)
	jobToLeafMap := make(map[string]string)
	for _, operatorSigningResult := range operatorSigningResults {
		leafData := leafDataMap[operatorSigningResult.LeafId]
		refundTxSighash, _ := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[0])
		nonceProto, _ := leafData.Nonce.MarshalProto()
		nonceCommitmentProto, _ := leafData.Nonce.SigningCommitment().MarshalProto()
		userKeyPackage := CreateUserKeyPackage(leafData.SigningPrivKey)

		userSigningJobID := uuid.NewString()
		jobToLeafMap[userSigningJobID] = operatorSigningResult.LeafId
		userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
			JobId:            userSigningJobID,
			Message:          refundTxSighash,
			KeyPackage:       userKeyPackage,
			VerifyingKey:     operatorSigningResult.VerifyingKey,
			Nonce:            nonceProto,
			Commitments:      operatorSigningResult.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments:  nonceCommitmentProto,
			AdaptorPublicKey: adaptorPublicKeyBytes,
		})

		jobToAggregateRequestMap[userSigningJobID] = &pbfrost.AggregateFrostRequest{
			Message:          refundTxSighash,
			SignatureShares:  operatorSigningResult.RefundTxSigningResult.SignatureShares,
			PublicShares:     operatorSigningResult.RefundTxSigningResult.PublicKeys,
			VerifyingKey:     operatorSigningResult.VerifyingKey,
			Commitments:      operatorSigningResult.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments:  nonceCommitmentProto,
			UserPublicKey:    leafData.SigningPrivKey.Public().Serialize(),
			AdaptorPublicKey: adaptorPublicKeyBytes,
		}
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

	var nodeSignatures []*pb.NodeSignatures
	for jobID, userSignature := range userSignatures.Results {
		request := jobToAggregateRequestMap[jobID]
		request.UserSignatureShare = userSignature.SignatureShare
		response, err := frostClient.AggregateFrost(context.Background(), request)
		if err != nil {
			return nil, err
		}
		nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
			NodeId:            jobToLeafMap[jobID],
			RefundTxSignature: response.Signature,
		})
	}
	return nodeSignatures, nil
}

func prepareRefundSoSigningJobs(
	leaves []LeafKeyTweak,
	leafDataMap map[string]*LeafRefundSigningData,
) ([]*pb.LeafRefundTxSigningJob, error) {
	var signingJobs []*pb.LeafRefundTxSigningJob
	for _, leaf := range leaves {
		refundSigningData := leafDataMap[leaf.Leaf.Id]
		nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node tx: %w", err)
		}
		nodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
		currRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse refund tx: %w", err)
		}
		amountSats := nodeTx.TxOut[0].Value
		nextSequence, err := spark.NextSequence(currRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		cpfpRefundTx, _, err := createRefundTxs(nextSequence, &nodeOutPoint, amountSats, refundSigningData.ReceivingPubKey, true)
		if err != nil {
			return nil, fmt.Errorf("failed to create refund tx: %w", err)
		}
		refundSigningData.RefundTx = cpfpRefundTx
		var refundBuf bytes.Buffer
		err = cpfpRefundTx.Serialize(&refundBuf)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize refund tx: %w", err)
		}
		refundNonceCommitmentProto, _ := refundSigningData.Nonce.SigningCommitment().MarshalProto()

		signingJobs = append(signingJobs, &pb.LeafRefundTxSigningJob{
			LeafId: leaf.Leaf.Id,
			RefundTxSigningJob: &pb.SigningJob{
				SigningPublicKey:       refundSigningData.SigningPrivKey.Public().Serialize(),
				RawTx:                  refundBuf.Bytes(),
				SigningNonceCommitment: refundNonceCommitmentProto,
			},
		})
	}
	return signingJobs, nil
}

func CancelTransfer(ctx context.Context, config *TestWalletConfig, transfer *pb.Transfer) (*pb.Transfer, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.CancelTransfer(authCtx, &pb.CancelTransferRequest{
		TransferId:              transfer.Id,
		SenderIdentityPublicKey: config.IdentityPublicKey().Serialize(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to call CancelTransfer: %w", err)
	}
	return response.Transfer, nil
}

func QueryAllTransfers(ctx context.Context, config *TestWalletConfig, limit int64, offset int64) ([]*pb.Transfer, int64, error) {
	return QueryAllTransfersWithTypes(ctx, config, limit, offset, []pb.TransferType{})
}

func QueryAllTransfersWithTypes(ctx context.Context, config *TestWalletConfig, limit int64, offset int64, types []pb.TransferType) ([]*pb.Transfer, int64, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, 0, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)
	network, err := common.ProtoNetworkFromNetwork(config.Network)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to convert network to proto network: %w", err)
	}
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.QueryAllTransfers(authCtx, &pb.TransferFilter{
		Participant: &pb.TransferFilter_SenderOrReceiverIdentityPublicKey{
			SenderOrReceiverIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		},
		Limit:   limit,
		Offset:  offset,
		Types:   types,
		Network: network,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to call QueryAllTransfers: %w", err)
	}
	return response.GetTransfers(), response.GetOffset(), nil
}
