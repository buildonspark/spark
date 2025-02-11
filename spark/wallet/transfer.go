package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/objects"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LeafKeyTweak is a struct to hold leaf key to tweak.
type LeafKeyTweak struct {
	Leaf              *pb.TreeNode
	SigningPrivKey    []byte
	NewSigningPrivKey []byte
}

// SendTransfer initiates a transfer from sender.
func SendTransfer(
	ctx context.Context,
	config *Config,
	leaves []LeafKeyTweak,
	receiverIdentityPubkey []byte,
	expiryTime time.Time,
) (*pb.Transfer, error) {
	transfer, refundSignatureMap, _, err := SendTransferSignRefund(ctx, config, leaves, receiverIdentityPubkey, expiryTime)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refund: %v", err)
	}
	transfer, err = SendTransferTweakKey(ctx, config, transfer, leaves, refundSignatureMap)
	if err != nil {
		return nil, fmt.Errorf("failed to tweak key: %v", err)
	}
	return transfer, nil
}

func SendTransferTweakKey(
	ctx context.Context,
	config *Config,
	transfer *pb.Transfer,
	leaves []LeafKeyTweak,
	refundSignatureMap map[string][]byte,
) (*pb.Transfer, error) {
	keyTweakInputMap, err := prepareSendTransferKeyTweaks(config, transfer, leaves, refundSignatureMap)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %v", err)
	}

	var updatedTransfer *pb.Transfer
	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))
	for identifier, operator := range config.SigningOperators {
		wg.Add(1)
		go func(identifier string, operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
			if err != nil {
				results <- err
				return
			}
			defer sparkConn.Close()
			sparkClient := pb.NewSparkServiceClient(sparkConn)
			token, err := AuthenticateWithConnection(ctx, config, sparkConn)
			if err != nil {
				results <- fmt.Errorf("failed to authenticate with server: %v", err)
				return
			}
			tmpCtx := ContextWithToken(ctx, token)
			transferResp, err := sparkClient.CompleteSendTransfer(tmpCtx, &pb.CompleteSendTransferRequest{
				TransferId:             transfer.Id,
				OwnerIdentityPublicKey: config.IdentityPublicKey(),
				LeavesToSend:           (*keyTweakInputMap)[identifier],
			})
			if err != nil {
				results <- fmt.Errorf("failed to call SendTransfer: %v", err)
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

func SendSwapSignRefund(
	ctx context.Context,
	config *Config,
	leaves []LeafKeyTweak,
	receiverIdentityPubkey []byte,
	expiryTime time.Time,
	adaptorPublicKey *secp256k1.PublicKey,
) (*pb.Transfer, map[string][]byte, map[string]*LeafRefundSigningData, []*pb.LeafRefundTxSigningResult, error) {
	transferID, err := uuid.NewRandom()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to generate transfer id: %v", err)
	}

	leafDataMap := make(map[string]*LeafRefundSigningData)
	for _, leafKey := range leaves {
		privKey := secp256k1.PrivKeyFromBytes(leafKey.SigningPrivKey)
		nonce, _ := objects.RandomSigningNonce()
		tx, _ := common.TxFromRawTxBytes(leafKey.Leaf.NodeTx)
		leafDataMap[leafKey.Leaf.Id] = &LeafRefundSigningData{
			SigningPrivKey:  privKey,
			ReceivingPubkey: receiverIdentityPubkey,
			Nonce:           nonce,
			Tx:              tx,
			Vout:            int(leafKey.Leaf.Vout),
		}
	}

	signingJobs, err := prepareRefundSoSigningJobs(leaves, config, leafDataMap)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to prepare signing jobs for sending transfer: %v", err)
	}

	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.LeafSwap(tmpCtx, &pb.LeafSwapRequest{
		Transfer: &pb.StartSendTransferRequest{
			TransferId:                transferID.String(),
			LeavesToSend:              signingJobs,
			OwnerIdentityPublicKey:    config.IdentityPublicKey(),
			ReceiverIdentityPublicKey: receiverIdentityPubkey,
			ExpiryTime:                timestamppb.New(expiryTime),
		},
		SwapId:           uuid.New().String(),
		AdaptorPublicKey: adaptorPublicKey.SerializeCompressed(),
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to start send transfer: %v", err)
	}

	signatures, err := signRefunds(config, leafDataMap, response.SigningResults, adaptorPublicKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to sign refunds for send: %v", err)
	}
	signatureMap := make(map[string][]byte)
	for _, signature := range signatures {
		signatureMap[signature.NodeId] = signature.RefundTxSignature
	}
	return response.Transfer, signatureMap, leafDataMap, response.SigningResults, nil
}

func SendTransferSignRefund(
	ctx context.Context,
	config *Config,
	leaves []LeafKeyTweak,
	receiverIdentityPubkey []byte,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, map[string]*LeafRefundSigningData, error) {
	transferID, err := uuid.NewRandom()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to generate transfer id: %v", err)
	}

	leafDataMap := make(map[string]*LeafRefundSigningData)
	for _, leafKey := range leaves {
		privKey := secp256k1.PrivKeyFromBytes(leafKey.SigningPrivKey)
		nonce, _ := objects.RandomSigningNonce()
		tx, _ := common.TxFromRawTxBytes(leafKey.Leaf.NodeTx)
		leafDataMap[leafKey.Leaf.Id] = &LeafRefundSigningData{
			SigningPrivKey:  privKey,
			ReceivingPubkey: receiverIdentityPubkey,
			Nonce:           nonce,
			Tx:              tx,
			Vout:            int(leafKey.Leaf.Vout),
		}
	}

	signingJobs, err := prepareRefundSoSigningJobs(leaves, config, leafDataMap)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to prepare signing jobs for sending transfer: %v", err)
	}

	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		return nil, nil, nil, err
	}
	defer sparkConn.Close()

	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.StartSendTransfer(tmpCtx, &pb.StartSendTransferRequest{
		TransferId:                transferID.String(),
		LeavesToSend:              signingJobs,
		OwnerIdentityPublicKey:    config.IdentityPublicKey(),
		ReceiverIdentityPublicKey: receiverIdentityPubkey,
		ExpiryTime:                timestamppb.New(expiryTime),
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to start send transfer: %v", err)
	}

	signatures, err := signRefunds(config, leafDataMap, response.SigningResults, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to sign refunds for send: %v", err)
	}
	signatureMap := make(map[string][]byte)
	for _, signature := range signatures {
		signatureMap[signature.NodeId] = signature.RefundTxSignature
	}
	return response.Transfer, signatureMap, leafDataMap, nil
}

func compareTransfers(transfer1, transfer2 *pb.Transfer) bool {
	return transfer1.Id == transfer2.Id &&
		bytes.Equal(transfer1.ReceiverIdentityPublicKey, transfer2.ReceiverIdentityPublicKey) &&
		transfer1.Status == transfer2.Status &&
		transfer1.TotalValue == transfer2.TotalValue &&
		transfer1.ExpiryTime.AsTime().Equal(transfer2.ExpiryTime.AsTime()) &&
		len(transfer1.Leaves) == len(transfer2.Leaves)
}

func prepareSendTransferKeyTweaks(config *Config, transfer *pb.Transfer, leaves []LeafKeyTweak, refundSignatureMap map[string][]byte) (*map[string][]*pb.SendLeafKeyTweak, error) {
	receiverEciesPubKey, err := eciesgo.NewPublicKeyFromBytes(transfer.ReceiverIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse receiver public key: %v", err)
	}

	leavesTweaksMap := make(map[string][]*pb.SendLeafKeyTweak)
	for _, leaf := range leaves {
		leafTweaksMap, err := prepareSingleSendTransferKeyTweak(config, transfer.Id, leaf, receiverEciesPubKey, refundSignatureMap[leaf.Leaf.Id])
		if err != nil {
			return nil, fmt.Errorf("failed to prepare single leaf transfer: %v", err)
		}
		for identifier, leafTweak := range *leafTweaksMap {
			leavesTweaksMap[identifier] = append(leavesTweaksMap[identifier], leafTweak)
		}
	}
	return &leavesTweaksMap, nil
}

func prepareSingleSendTransferKeyTweak(config *Config, transferID string, leaf LeafKeyTweak, receiverEciesPubKey *eciesgo.PublicKey, refundSignature []byte) (*map[string]*pb.SendLeafKeyTweak, error) {
	privKeyTweak, err := common.SubtractPrivateKeys(leaf.SigningPrivKey, leaf.NewSigningPrivKey)
	if err != nil {
		return nil, fmt.Errorf("fail to calculate private key tweak: %v", err)
	}

	// Calculate secret tweak shares
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to split private key tweak: %v", err)
	}

	// Calculate pubkey shares tweak
	pubkeySharesTweak := make(map[string][]byte)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		var shareScalar secp256k1.ModNScalar
		shareScalar.SetByteSlice(share.Share.Bytes())
		pubkeyTweak := secp256k1.NewPrivateKey(&shareScalar).PubKey()
		pubkeySharesTweak[identifier] = pubkeyTweak.SerializeCompressed()
	}

	secretCipher, err := eciesgo.Encrypt(receiverEciesPubKey, leaf.NewSigningPrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt new signing private key: %v", err)
	}

	// Generate signature over Sha256(leaf_id||transfer_id||secret_cipher)
	payload := append(append([]byte(leaf.Leaf.Id), []byte(transferID)...), secretCipher...)
	payloadHash := sha256.Sum256(payload)
	signature := ecdsa.Sign(&config.IdentityPrivateKey, payloadHash[:])

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
	return &leafTweaksMap, nil
}

func findShare(shares []*secretsharing.VerifiableSecretShare, operatorID uint64) *secretsharing.VerifiableSecretShare {
	targetShareIndex := big.NewInt(int64(operatorID + 1))
	for _, s := range shares {
		if s.SecretShare.Index.Cmp(targetShareIndex) == 0 {
			return s
		}
	}
	return nil
}

// QueryPendingTransfers queries pending transfers to claim.
func QueryPendingTransfers(
	ctx context.Context,
	config *Config,
) (*pb.QueryPendingTransfersResponse, error) {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()

	sparkClient := pb.NewSparkServiceClient(sparkConn)
	return sparkClient.QueryPendingTransfers(ctx, &pb.QueryPendingTransfersRequest{
		ReceiverIdentityPublicKey: config.IdentityPublicKey(),
	})
}

// VerifyPendingTransfer verifies signature and decrypt secret cipher for all leaves in the transfer.
func VerifyPendingTransfer(
	ctx context.Context,
	config *Config,
	transfer *pb.Transfer,
) (*map[string][]byte, error) {
	leafPrivKeyMap := make(map[string][]byte)
	senderPubkey, err := secp256k1.ParsePubKey(transfer.SenderIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sender public key: %v", err)
	}

	receiverEciesPrivKey := eciesgo.NewPrivateKeyFromBytes(config.IdentityPrivateKey.Serialize())
	for _, leaf := range transfer.Leaves {
		// Verify signature
		signature, err := ecdsa.ParseDERSignature(leaf.Signature)
		if err != nil {
			return nil, fmt.Errorf("failed to parse signature: %v", err)
		}
		payload := append(append([]byte(leaf.Leaf.Id), []byte(transfer.Id)...), leaf.SecretCipher...)
		payloadHash := sha256.Sum256(payload)
		if !signature.Verify(payloadHash[:], senderPubkey) {
			return nil, fmt.Errorf("failed to verify signature: %v", err)
		}

		// Decrypt secret cipher
		leafSecret, err := eciesgo.Decrypt(receiverEciesPrivKey, leaf.SecretCipher)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt secret cipher: %v", err)
		}
		leafPrivKeyMap[leaf.Leaf.Id] = leafSecret

	}
	return &leafPrivKeyMap, nil
}

// ClaimTransfer claims a pending transfer.
func ClaimTransfer(
	ctx context.Context,
	transfer *pb.Transfer,
	config *Config,
	leaves []LeafKeyTweak,
) error {
	err := claimTransferTweakKeys(ctx, transfer, config, leaves)
	if err != nil {
		return fmt.Errorf("failed to tweak keys when claiming leaves: %v", err)
	}

	signatures, err := claimTransferSignRefunds(ctx, transfer, config, leaves)
	if err != nil {
		return fmt.Errorf("failed to sign refunds when claiming leaves: %v", err)
	}

	return finalizeTransfer(ctx, config, signatures)
}

func claimTransferTweakKeys(
	ctx context.Context,
	transfer *pb.Transfer,
	config *Config,
	leaves []LeafKeyTweak,
) error {
	leavesTweaksMap, err := prepareClaimLeavesKeyTweaks(config, leaves)
	if err != nil {
		return fmt.Errorf("failed to prepare transfer data: %v", err)
	}

	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))

	for identifier, operator := range config.SigningOperators {
		wg.Add(1)
		go func(identifier string, operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := common.NewGRPCConnectionWithTestTLS(operator.Address)
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
				OwnerIdentityPublicKey: config.IdentityPublicKey(),
				LeavesToReceive:        (*leavesTweaksMap)[identifier],
			})
			if err != nil {
				results <- fmt.Errorf("failed to call ClaimTransferTweakKeys: %v", err)
			}
		}(identifier, operator)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result != nil {
			return result
		}
	}
	return nil
}

func prepareClaimLeavesKeyTweaks(config *Config, leaves []LeafKeyTweak) (*map[string][]*pb.ClaimLeafKeyTweak, error) {
	leavesTweaksMap := make(map[string][]*pb.ClaimLeafKeyTweak)
	for _, leaf := range leaves {
		leafTweaksMap, err := prepareClaimLeafKeyTweaks(config, leaf)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare single leaf transfer: %v", err)
		}
		for identifier, leafTweak := range *leafTweaksMap {
			leavesTweaksMap[identifier] = append(leavesTweaksMap[identifier], leafTweak)
		}
	}
	return &leavesTweaksMap, nil
}

func prepareClaimLeafKeyTweaks(config *Config, leaf LeafKeyTweak) (*map[string]*pb.ClaimLeafKeyTweak, error) {
	privKeyTweak, err := common.SubtractPrivateKeys(leaf.SigningPrivKey, leaf.NewSigningPrivKey)
	if err != nil {
		return nil, fmt.Errorf("fail to calculate private key tweak: %v", err)
	}

	// Calculate secret tweak shares
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to split private key tweak: %v", err)
	}

	// Calculate pubkey shares tweak
	pubkeySharesTweak := make(map[string][]byte)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
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
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
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
	return &leafTweaksMap, nil
}

type LeafRefundSigningData struct {
	SigningPrivKey  *secp256k1.PrivateKey
	ReceivingPubkey []byte
	Tx              *wire.MsgTx
	RefundTx        *wire.MsgTx
	Nonce           *objects.SigningNonce
	Vout            int
}

func claimTransferSignRefunds(
	ctx context.Context,
	transfer *pb.Transfer,
	config *Config,
	leafKeys []LeafKeyTweak,
) ([]*pb.NodeSignatures, error) {
	leafDataMap := make(map[string]*LeafRefundSigningData)
	for _, leafKey := range leafKeys {
		privKey := secp256k1.PrivKeyFromBytes(leafKey.NewSigningPrivKey)
		nonce, _ := objects.RandomSigningNonce()
		tx, _ := common.TxFromRawTxBytes(leafKey.Leaf.NodeTx)
		leafDataMap[leafKey.Leaf.Id] = &LeafRefundSigningData{
			SigningPrivKey:  privKey,
			ReceivingPubkey: privKey.PubKey().SerializeCompressed(),
			Nonce:           nonce,
			Tx:              tx,
			Vout:            int(leafKey.Leaf.Vout),
		}
	}

	signingJobs, err := prepareRefundSoSigningJobs(leafKeys, config, leafDataMap)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare signing jobs for claiming transfer: %v", err)
	}
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.ClaimTransferSignRefunds(ctx, &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to call ClaimTransferSignRefunds: %v", err)
	}

	return signRefunds(config, leafDataMap, response.SigningResults, nil)
}

func finalizeTransfer(
	ctx context.Context,
	config *Config,
	signatures []*pb.NodeSignatures,
) error {
	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress())
	if err != nil {
		return err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	_, err = sparkClient.FinalizeNodeSignatures(ctx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_TRANSFER,
		NodeSignatures: signatures,
	})
	return err
}

func signRefunds(
	config *Config,
	leafDataMap map[string]*LeafRefundSigningData,
	operatorSigningResults []*pb.LeafRefundTxSigningResult,
	adaptorPublicKey *secp256k1.PublicKey,
) ([]*pb.NodeSignatures, error) {
	var adaptorPublicKeyBytes []byte
	if adaptorPublicKey != nil {
		adaptorPublicKeyBytes = adaptorPublicKey.SerializeCompressed()
	}

	userSigningJobs := []*pbfrost.FrostSigningJob{}
	jobToAggregateRequestMap := make(map[string]*pbfrost.AggregateFrostRequest)
	jobToLeafMap := make(map[string]string)
	for _, operatorSigningResult := range operatorSigningResults {
		leafData := leafDataMap[operatorSigningResult.LeafId]
		refundTxSighash, _ := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
		nonceProto, _ := leafData.Nonce.MarshalProto()
		nonceCommitmentProto, _ := leafData.Nonce.SigningCommitment().MarshalProto()
		userKeyPackage := CreateUserKeyPackage(leafData.SigningPrivKey.Serialize())

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
			UserPublicKey:    leafData.SigningPrivKey.PubKey().SerializeCompressed(),
			AdaptorPublicKey: adaptorPublicKeyBytes,
		}
	}

	frostConn, _ := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress)
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	nodeSignatures := []*pb.NodeSignatures{}
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
	config *Config,
	leafDataMap map[string]*LeafRefundSigningData,
) ([]*pb.LeafRefundTxSigningJob, error) {
	signingJobs := []*pb.LeafRefundTxSigningJob{}
	for _, leaf := range leaves {
		refundSigningData := leafDataMap[leaf.Leaf.Id]
		nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node tx: %v", err)
		}
		nodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
		currRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse refund tx: %v", err)
		}
		amountSats := nodeTx.TxOut[0].Value
		receivingPubkey, err := secp256k1.ParsePubKey(refundSigningData.ReceivingPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse receiving pubkey: %v", err)
		}
		nextSequence, err := nextSequence(currRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, fmt.Errorf("failed to get next sequence: %v", err)
		}
		refundTx, err := createRefundTx(nextSequence, &nodeOutPoint, amountSats, receivingPubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to create refund tx: %v", err)
		}
		refundSigningData.RefundTx = refundTx
		var refundBuf bytes.Buffer
		refundTx.Serialize(&refundBuf)
		refundNonceCommitmentProto, _ := refundSigningData.Nonce.SigningCommitment().MarshalProto()

		signingPubkey := refundSigningData.SigningPrivKey.PubKey().SerializeCompressed()
		signingJobs = append(signingJobs, &pb.LeafRefundTxSigningJob{
			LeafId: leaf.Leaf.Id,
			RefundTxSigningJob: &pb.SigningJob{
				SigningPublicKey:       signingPubkey,
				RawTx:                  refundBuf.Bytes(),
				SigningNonceCommitment: refundNonceCommitmentProto,
			},
		})
	}
	return signingJobs, nil
}
