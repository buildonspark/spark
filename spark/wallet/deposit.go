package wallet

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

// GenerateDepositAddress generates a deposit address for a given identity and signing public key.
func GenerateDepositAddress(
	ctx context.Context,
	config *Config,
	identityPubkey, signingPubkey []byte,
) (*pb.GenerateDepositAddressResponse, error) {
	sparkConn, err := common.NewGRPCConnection(config.SparkServiceAddress)
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	depositResp, err := sparkClient.GenerateDepositAddress(ctx, &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  signingPubkey,
		IdentityPublicKey: identityPubkey,
	})
	if err != nil {
		return nil, err
	}
	return depositResp, nil
}

// CreateTree creates a tree for a given deposit transaction.
func CreateTree(
	ctx context.Context,
	config *Config,
	identityPubkey,
	signingPrivKey,
	verifyingKey []byte,
	depositTx *wire.MsgTx,
	vout int,
) (*pb.FinalizeNodeSignaturesResponse, error) {
	_, signingPubkey := secp256k1.PrivKeyFromBytes(signingPrivKey)
	signingPubkeyBytes := signingPubkey.SerializeCompressed()
	// Creat root tx
	rootTx := wire.NewMsgTx(2)
	rootTx.AddTxIn(wire.NewTxIn(
		&wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)},
		depositTx.TxOut[0].PkScript,
		nil, // witness
	))
	rootTx.AddTxOut(wire.NewTxOut(100_000, depositTx.TxOut[0].PkScript))
	var rootBuf bytes.Buffer
	rootTx.Serialize(&rootBuf)
	rootNonceHidingPriv, _ := secp256k1.GeneratePrivateKey()
	rootNonceBidingPriv, _ := secp256k1.GeneratePrivateKey()
	rootNonceCommitment := pbcommon.SigningCommitment{
		Hiding:  rootNonceHidingPriv.PubKey().SerializeCompressed(),
		Binding: rootNonceBidingPriv.PubKey().SerializeCompressed(),
	}
	rootTxSighash, err := common.SigHashFromTx(rootTx, 0, depositTx.TxOut[0])
	if err != nil {
		return nil, err
	}

	// Creat refund tx
	refundTx := wire.NewMsgTx(2)
	refundTx.AddTxIn(wire.NewTxIn(
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].PkScript,
		nil, // witness
	))
	refundP2trAddress, _ := common.P2TRAddressFromPublicKey(signingPubkeyBytes, config.Network)
	refundAddress, _ := btcutil.DecodeAddress(*refundP2trAddress, common.NetworkParams(config.Network))
	refundPkScript, _ := txscript.PayToAddrScript(refundAddress)
	refundTx.AddTxOut(wire.NewTxOut(100_000, refundPkScript))
	refundTx.LockTime = 60000
	var refundBuf bytes.Buffer
	refundTx.Serialize(&refundBuf)
	refundNonceHidingPriv, _ := secp256k1.GeneratePrivateKey()
	refundNonceBidingPriv, _ := secp256k1.GeneratePrivateKey()
	refundNonceCommitment := pbcommon.SigningCommitment{
		Hiding:  refundNonceHidingPriv.PubKey().SerializeCompressed(),
		Binding: refundNonceBidingPriv.PubKey().SerializeCompressed(),
	}
	refundTxSighash, err := common.SigHashFromTx(refundTx, 0, rootTx.TxOut[0])
	if err != nil {
		return nil, err
	}

	sparkConn, err := common.NewGRPCConnection(config.SparkServiceAddress)
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)

	treeResponse, err := sparkClient.StartTreeCreation(ctx, &pb.StartTreeCreationRequest{
		IdentityPublicKey: identityPubkey,
		OnChainUtxo: &pb.UTXO{
			Txid: depositTx.TxID(),
			Vout: uint32(vout),
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx:                  rootBuf.Bytes(),
			SigningPublicKey:       signingPubkeyBytes,
			SigningNonceCommitment: &rootNonceCommitment,
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:                  refundBuf.Bytes(),
			SigningPublicKey:       signingPubkeyBytes,
			SigningNonceCommitment: &refundNonceCommitment,
		},
	})
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(treeResponse.RootNodeSignatureShares.VerifyingKey, verifyingKey) {
		return nil, fmt.Errorf("verifying key does not match")
	}

	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	userKeyPackage := pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: signingPrivKey,
		PublicShares: map[string][]byte{
			userIdentifier: signingPubkeyBytes,
		},
		PublicKey:  treeResponse.RootNodeSignatureShares.VerifyingKey,
		MinSigners: 1,
	}

	userSigningJobs := make([]*pbfrost.FrostSigningJob, 0)
	nodeJobID := uuid.NewString()
	refundJobID := uuid.NewString()
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:        nodeJobID,
		Message:      rootTxSighash,
		KeyPackage:   &userKeyPackage,
		VerifyingKey: verifyingKey,
		Nonce: &pbfrost.SigningNonce{
			Hiding:  rootNonceHidingPriv.Serialize(),
			Binding: rootNonceBidingPriv.Serialize(),
		},
		Commitments:     treeResponse.RootNodeSignatureShares.NodeTxSigningResult.SigningNonceCommitments,
		UserCommitments: &rootNonceCommitment,
	})
	userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
		JobId:        refundJobID,
		Message:      refundTxSighash,
		KeyPackage:   &userKeyPackage,
		VerifyingKey: treeResponse.RootNodeSignatureShares.VerifyingKey,
		Nonce: &pbfrost.SigningNonce{
			Hiding:  refundNonceHidingPriv.Serialize(),
			Binding: refundNonceBidingPriv.Serialize(),
		},
		Commitments:     treeResponse.RootNodeSignatureShares.RefundTxSigningResult.SigningNonceCommitments,
		UserCommitments: &refundNonceCommitment,
	})

	frostConn, err := common.NewGRPCConnection(config.FrostSignerAddress)
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
		UserCommitments:    &rootNonceCommitment,
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
		UserCommitments:    &refundNonceCommitment,
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
