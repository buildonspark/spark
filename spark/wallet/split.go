package wallet

import (
	"bytes"
	"context"
	"fmt"
	"log"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/objects"
)

// SplitResult is the result of a split operation.
type SplitResult struct {
	// Nodes is the list of new nodes created by the split operation.
	Nodes []*pb.TreeNode
	// SigningPrivKeys is the list of private keys for the splits.
	SigningPrivKeys [][]byte
}

func prepareKeys(targetKey []byte) ([][]byte, error) {
	keys := make([][]byte, 0)
	leftKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	keys = append(keys, leftKey.Serialize())
	rightKey, err := common.LastKeyWithTarget(targetKey, keys)
	if err != nil {
		return nil, err
	}
	keys = append(keys, rightKey)
	return keys, nil
}

func createSplitTx(ctx context.Context, config *Config, node *pb.TreeNode, addrResp *pb.PrepareSplitAddressResponse, amounts []int64) (*wire.MsgTx, []byte, error) {
	if len(amounts) != len(addrResp.Addresses) {
		return nil, nil, fmt.Errorf("amounts and addresses length mismatch")
	}
	splitTx := wire.NewMsgTx(2)
	nodeTx, err := common.TxFromRawTxBytes(node.NodeTx)
	if err != nil {
		return nil, nil, err
	}
	splitTx.AddTxIn(wire.NewTxIn(
		&wire.OutPoint{Hash: nodeTx.TxHash(), Index: node.Vout},
		nodeTx.TxOut[node.Vout].PkScript,
		nil, // witness
	))
	for i, amount := range amounts {
		refundAddress, _ := btcutil.DecodeAddress(addrResp.Addresses[i].Address, common.NetworkParams(config.Network))
		refundPkScript, _ := txscript.PayToAddrScript(refundAddress)
		splitTx.AddTxOut(wire.NewTxOut(amount, refundPkScript))
	}

	splitTxSighash, err := common.SigHashFromTx(splitTx, 0, nodeTx.TxOut[node.Vout])
	if err != nil {
		return nil, nil, err
	}
	return splitTx, splitTxSighash, nil
}

func prepareSplits(ctx context.Context, config *Config, node *pb.TreeNode, splitTx *wire.MsgTx, childrenPubkeys [][]byte) ([]*pb.Split, [][]byte, []*objects.SigningNonce, error) {
	splits := make([]*pb.Split, 0)
	signingNonces := make([]*objects.SigningNonce, 0)
	sighashes := make([][]byte, 0)
	for i, output := range splitTx.TxOut {
		refundTx := wire.NewMsgTx(2)
		refundP2trAddress, _ := common.P2TRAddressFromPublicKey(childrenPubkeys[i], config.Network)
		refundAddress, _ := btcutil.DecodeAddress(*refundP2trAddress, common.NetworkParams(config.Network))
		refundPkScript, _ := txscript.PayToAddrScript(refundAddress)
		refundTx.AddTxIn(wire.NewTxIn(
			&wire.OutPoint{Hash: splitTx.TxHash(), Index: uint32(i)},
			refundPkScript,
			nil, // witness
		))
		refundTx.AddTxOut(wire.NewTxOut(output.Value, output.PkScript))
		refundTx.LockTime = 60000
		var refundBuf bytes.Buffer
		refundTx.Serialize(&refundBuf)
		sigHash, err := common.SigHashFromTx(refundTx, 0, output)
		if err != nil {
			return nil, nil, nil, err
		}
		sighashes = append(sighashes, sigHash)

		signingNonce, err := objects.RandomSigningNonce()
		if err != nil {
			return nil, nil, nil, err
		}
		signingNonceCommitmentProto, err := signingNonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, nil, nil, err
		}
		signingNonces = append(signingNonces, signingNonce)

		signingJob := &pb.SigningJob{
			SigningPublicKey:       childrenPubkeys[i],
			RawTx:                  refundBuf.Bytes(),
			SigningNonceCommitment: signingNonceCommitmentProto,
		}
		split := &pb.Split{
			SigningPublicKey: childrenPubkeys[i],
			RefundSigningJob: signingJob,
			Value:            uint64(output.Value),
			Vout:             uint32(i),
		}
		splits = append(splits, split)
	}
	return splits, sighashes, signingNonces, nil
}

// SplitTreeNode splits a tree node into two nodes, and returns the new nodes and the keys for the splits.
func SplitTreeNode(
	ctx context.Context,
	config *Config,
	node *pb.TreeNode,
	leftAmount int64,
	parentSigningPrivKey []byte,
) (*pb.FinalizeNodeSignaturesResponse, error) {
	_, parentPubkey := secp256k1.PrivKeyFromBytes(parentSigningPrivKey)
	childrenKeys, err := prepareKeys(parentSigningPrivKey)
	if err != nil {
		return nil, err
	}

	childrenPubKeys := make([][]byte, 0)
	for _, key := range childrenKeys {
		_, pubkey := secp256k1.PrivKeyFromBytes(key)
		childrenPubKeys = append(childrenPubKeys, pubkey.SerializeCompressed())
	}

	sparkConn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	addrResp, err := sparkClient.PrepareSplitAddress(ctx, &pb.PrepareSplitAddressRequest{
		NodeId:            node.Id,
		SigningPublicKeys: childrenPubKeys,
	})
	if err != nil {
		return nil, err
	}

	amounts := []int64{leftAmount, int64(node.Value) - leftAmount}
	splitTx, splitTxSighash, err := createSplitTx(ctx, config, node, addrResp, amounts)
	if err != nil {
		return nil, err
	}
	splitTxNonce, err := objects.RandomSigningNonce()
	if err != nil {
		return nil, err
	}
	splitTxNonceProto, err := splitTxNonce.MarshalProto()
	if err != nil {
		return nil, err
	}
	splitTxNonceCommitmentProto, err := splitTxNonce.SigningCommitment().MarshalProto()
	if err != nil {
		return nil, err
	}
	var splitBuf bytes.Buffer
	splitTx.Serialize(&splitBuf)

	splitTxSigningJob := &pb.SigningJob{
		SigningPublicKey:       parentPubkey.SerializeCompressed(),
		RawTx:                  splitBuf.Bytes(),
		SigningNonceCommitment: splitTxNonceCommitmentProto,
	}

	splits, sighashes, signingNonces, err := prepareSplits(ctx, config, node, splitTx, childrenPubKeys)
	if err != nil {
		return nil, err
	}

	splitResp, err := sparkClient.SplitNode(ctx, &pb.SplitNodeRequest{
		NodeId:             node.Id,
		ParentTxSigningJob: splitTxSigningJob,
		Splits:             splits,
	})
	if err != nil {
		return nil, err
	}

	log.Println("splitResp", splitResp)

	signingJobs := make([]*pbfrost.FrostSigningJob, 0)
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: parentSigningPrivKey,
		PublicShares: map[string][]byte{
			userIdentifier: parentPubkey.SerializeCompressed(),
		},
		PublicKey:  parentPubkey.SerializeCompressed(),
		MinSigners: 1,
	}

	parentSigningJob := &pbfrost.FrostSigningJob{
		JobId:           uuid.NewString(),
		Message:         splitTxSighash,
		KeyPackage:      &userKeyPackage,
		VerifyingKey:    node.VerifyingKey,
		Nonce:           splitTxNonceProto,
		Commitments:     splitResp.ParentTxSigningResult.SigningNonceCommitments,
		UserCommitments: splitTxNonceCommitmentProto,
	}
	signingJobs = append(signingJobs, parentSigningJob)
	for i, splitResult := range splitResp.SplitResults {
		nonceProto, err := signingNonces[i].MarshalProto()
		if err != nil {
			return nil, err
		}
		commitmentProto, err := signingNonces[i].SigningCommitment().MarshalProto()
		if err != nil {
			return nil, err
		}
		userKeyPackage := pbfrost.KeyPackage{
			Identifier:  userIdentifier,
			SecretShare: childrenKeys[i],
			PublicShares: map[string][]byte{
				userIdentifier: childrenPubKeys[i],
			},
			PublicKey:  childrenPubKeys[i],
			MinSigners: 1,
		}
		signingJobs = append(signingJobs, &pbfrost.FrostSigningJob{
			JobId:           uuid.NewString(),
			Message:         sighashes[i],
			KeyPackage:      &userKeyPackage,
			VerifyingKey:    addrResp.Addresses[i].VerifyingKey,
			Nonce:           nonceProto,
			Commitments:     splitResult.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments: commitmentProto,
		})
	}

	frostConn, err := common.NewGRPCConnection(config.FrostSignerAddress)
	if err != nil {
		return nil, err
	}
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)

	frostResp, err := frostClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	nodeSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
		Message:            splitTxSighash,
		SignatureShares:    splitResp.ParentTxSigningResult.SignatureShares,
		PublicShares:       splitResp.ParentTxSigningResult.PublicKeys,
		VerifyingKey:       node.VerifyingKey,
		Commitments:        splitResp.ParentTxSigningResult.SigningNonceCommitments,
		UserCommitments:    splitTxNonceCommitmentProto,
		UserPublicKey:      parentPubkey.SerializeCompressed(),
		UserSignatureShare: frostResp.Results[signingJobs[0].JobId].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	nodeSignatures := make([]*pb.NodeSignatures, 0)

	refundSignatures := make([][]byte, 0)
	for i, splitResult := range splitResp.SplitResults {

		commitmentProto, err := signingNonces[i].SigningCommitment().MarshalProto()
		if err != nil {
			return nil, err
		}
		refundSignature, err := frostClient.AggregateFrost(context.Background(), &pbfrost.AggregateFrostRequest{
			Message:            sighashes[i],
			SignatureShares:    splitResult.RefundTxSigningResult.SignatureShares,
			PublicShares:       splitResult.RefundTxSigningResult.PublicKeys,
			VerifyingKey:       splitResult.VerifyingKey,
			Commitments:        splitResult.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments:    commitmentProto,
			UserPublicKey:      childrenPubKeys[i],
			UserSignatureShare: frostResp.Results[signingJobs[i+1].JobId].SignatureShare,
		})
		if err != nil {
			log.Printf("refund signature error: %v", err)
			return nil, err
		}
		refundSignatures = append(refundSignatures, refundSignature.Signature)
		nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
			NodeId:            splitResp.SplitResults[i].NodeId,
			NodeTxSignature:   nodeSignature.Signature,
			RefundTxSignature: refundSignature.Signature,
		})
	}

	return sparkClient.FinalizeNodeSignatures(ctx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_SPLIT,
		NodeSignatures: nodeSignatures,
	})
}
