//
//  Deposit.swift
//  Spark
//
//  Created by Zhen Lu on 12/18/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import Foundation
import secp256k1

func generateDepositAddress(
    client: Spark_SparkServiceAsyncClient,
    identityPublicKey: Data,
    signingPublicKey: Data
) async throws -> Spark_Address {
    var request = Spark_GenerateDepositAddressRequest()
    print("Create Address: \(identityPublicKey.hexString())")
    request.identityPublicKey = identityPublicKey;
    request.signingPublicKey = signingPublicKey;
    let response = try await client.generate_deposit_address(request)
    return response.depositAddress;
}

func createTree(
    client: Spark_SparkServiceAsyncClient,
    onchainTx: Data,
    onchainTxId: String,
    vout: UInt32,
    address: String,
    network: String,
    signingPrivateKey: Data,
    identityPublicKey: Data,
    verifyingPublicKey: Data
) async throws -> Spark_FinalizeNodeSignaturesResponse {
    let signingPublicKey = try secp256k1.Signing.PrivateKey(dataRepresentation: signingPrivateKey).publicKey
    let nodeTxResult = try constructNodeTx(tx: onchainTx, vout: vout, address: address, locktime: 0)
    let refundTxResult = try constructRefundTx(
        tx: nodeTxResult.tx,
        vout: 0,
        pubkey: signingPublicKey.dataRepresentation,
        network: network,
        locktime: 60000
    )

    let keyPackage = KeyPackage(
        secretKey: signingPrivateKey,
        publicKey: signingPublicKey.dataRepresentation,
        verifyingKey: verifyingPublicKey
    )
    let rootNonce = try frostNonce(keyPackage: keyPackage)
    let refundNonce = try frostNonce(keyPackage: keyPackage)

    var request = Spark_StartDepositTreeCreationRequest()
    request.identityPublicKey = identityPublicKey

    request.onChainUtxo.txid = onchainTxId
    request.onChainUtxo.vout = vout

    request.rootTxSigningJob.rawTx = nodeTxResult.tx
    request.rootTxSigningJob.signingPublicKey = signingPublicKey.dataRepresentation
    request.rootTxSigningJob.signingNonceCommitment.hiding = rootNonce.commitment.hiding
    request.rootTxSigningJob.signingNonceCommitment.binding = rootNonce.commitment.binding

    request.refundTxSigningJob.rawTx = refundTxResult.tx
    request.refundTxSigningJob.signingPublicKey = signingPublicKey.dataRepresentation
    request.refundTxSigningJob.signingNonceCommitment.hiding = refundNonce.commitment.hiding
    request.refundTxSigningJob.signingNonceCommitment.binding = refundNonce.commitment.binding
    let response = try await client.start_deposit_tree_creation(request)

    let rootSECommitments = response.rootNodeSignatureShares.nodeTxSigningResult.signingNonceCommitments.mapValues {
        value in
        SigningCommitment(hiding: value.hiding, binding: value.binding)
    }

    let rootUserSig = try signFrost(
        msg: nodeTxResult.sighash,
        keyPackage: keyPackage,
        nonce: rootNonce.nonce,
        selfCommitment: rootNonce.commitment,
        statechainCommitments: rootSECommitments
    )

    let rootSig = try aggregateFrost(
        msg: nodeTxResult.sighash,
        statechainCommitments: rootSECommitments,
        selfCommitment: rootNonce.commitment,
        statechainSignatures: response.rootNodeSignatureShares.nodeTxSigningResult.signatureShares,
        selfSignature: rootUserSig,
        statechainPublicKeys: response.rootNodeSignatureShares.nodeTxSigningResult.publicKeys,
        selfPublicKey: signingPublicKey.dataRepresentation,
        verifyingKey: verifyingPublicKey
    )

    let refundSECommitments = response.rootNodeSignatureShares.refundTxSigningResult.signingNonceCommitments.mapValues {
        value in
        SigningCommitment(hiding: value.hiding, binding: value.binding)
    }

    let refundUserSig = try signFrost(
        msg: refundTxResult.sighash,
        keyPackage: keyPackage,
        nonce: refundNonce.nonce,
        selfCommitment: refundNonce.commitment,
        statechainCommitments: refundSECommitments
    )

    let refundSig = try aggregateFrost(
        msg: refundTxResult.sighash,
        statechainCommitments: refundSECommitments,
        selfCommitment: refundNonce.commitment,
        statechainSignatures: response.rootNodeSignatureShares.refundTxSigningResult.signatureShares,
        selfSignature: refundUserSig,
        statechainPublicKeys: response.rootNodeSignatureShares.refundTxSigningResult.publicKeys,
        selfPublicKey: signingPublicKey.dataRepresentation,
        verifyingKey: verifyingPublicKey
    )

    var finalizeRequest = Spark_FinalizeNodeSignaturesRequest()
    finalizeRequest.intent = .creation
    var nodeSignatures = Spark_NodeSignatures()
    nodeSignatures.nodeID = response.rootNodeSignatureShares.nodeID
    nodeSignatures.nodeTxSignature = rootSig
    nodeSignatures.refundTxSignature = refundSig
    finalizeRequest.nodeSignatures.append(nodeSignatures)

    return try await client.finalize_node_signatures(finalizeRequest)
}
