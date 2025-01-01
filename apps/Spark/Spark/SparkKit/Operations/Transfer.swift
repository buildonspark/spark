//
//  Transfer.swift
//  Spark
//
//  Created by Yun Yu on 12/22/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import BigInt
import Foundation
import SwiftProtobuf
import secp256k1

struct LeafKeyTweak {
    let leafId: String
    let signingPrivateKey: secp256k1.Signing.PrivateKey
    let newSigningPrivateKey: secp256k1.Signing.PrivateKey
}

func sendTransfer(
    signingOperatorMap: [String: SigningOperator],
    leaves: [LeafKeyTweak],
    expiryTime: Date,
    receiverIdentityPublicKey: Data,
    identityPrivateKey: secp256k1.Signing.PrivateKey,
    threshold: UInt32
) async throws -> Spark_Transfer {
    let transferId = UUID()
    let leafKeyTweaksMap = try prepareSendLeafKeyTweaks(
        signingOperatorMap: signingOperatorMap,
        transferId: transferId,
        leaves: leaves,
        receiverIdentityPublicKey: receiverIdentityPublicKey,
        identityPrivateKey: identityPrivateKey,
        threshold: threshold
    )
    var transfer: Spark_Transfer? = nil
    for (identifier, signingOperator) in signingOperatorMap {
        guard let leafKeyTweaks = leafKeyTweaksMap[identifier] else {
            throw SparkError(message: "Cannot find leaf key tweaks to send")
        }

        var request = Spark_SendTransferRequest()
        request.transferID = transferId.uuidString
        request.ownerIdentityPublicKey = identityPrivateKey.publicKey.dataRepresentation
        request.expiryTime = convertToProtobufTimestamp(date: expiryTime)
        request.leavesToSend = leafKeyTweaks
        request.receiverIdentityPublicKey = receiverIdentityPublicKey
        let response = try await signingOperator.client.send_transfer(request)
        if transfer == nil {
            transfer = response.transfer
        } else {
            if let existingTransfer = transfer {
                if existingTransfer != response.transfer {
                    throw SparkError(message: "Inconsistent send transfer responses from different signing operators")
                }
            }
        }
    }

    if let finalTransfer = transfer {
        return finalTransfer
    } else {
        throw SparkError(message: "No valid transfer found")
    }
}

private func convertToProtobufTimestamp(date: Date) -> Google_Protobuf_Timestamp {
    let seconds = Int64(date.timeIntervalSince1970)
    let nanos = Int32((date.timeIntervalSince1970 - Double(seconds)) * 1_000_000_000)
    var timestamp = Google_Protobuf_Timestamp()
    timestamp.seconds = seconds
    timestamp.nanos = nanos
    return timestamp
}

private func prepareSendLeafKeyTweaks(
    signingOperatorMap: [String: SigningOperator],
    transferId: UUID,
    leaves: [LeafKeyTweak],
    receiverIdentityPublicKey: Data,
    identityPrivateKey: secp256k1.Signing.PrivateKey,
    threshold: UInt32
) throws -> [String: [Spark_SendLeafKeyTweak]] {
    var leafKeyTweaksMap: [String: [Spark_SendLeafKeyTweak]] = [:]
    for leaf in leaves {
        let leafKeyTweakMap = try prepareSingleSendLeafKeyTweak(
            signingOperatorMap: signingOperatorMap,
            transferId: transferId,
            leaf: leaf,
            receiverIdentityPublicKey: receiverIdentityPublicKey,
            identityPrivateKey: identityPrivateKey,
            threshold: threshold
        )
        for (identifier, leafTweak) in leafKeyTweakMap {
            leafKeyTweaksMap[identifier, default: []].append(leafTweak)
        }
    }
    return leafKeyTweaksMap
}

private func prepareSingleSendLeafKeyTweak(
    signingOperatorMap: [String: SigningOperator],
    transferId: UUID,
    leaf: LeafKeyTweak,
    receiverIdentityPublicKey: Data,
    identityPrivateKey: secp256k1.Signing.PrivateKey,
    threshold: UInt32
) throws -> [String: Spark_SendLeafKeyTweak] {
    let privateKeyTweak = try leaf.signingPrivateKey.subtract(leaf.newSigningPrivateKey)

    let shares = try splitSecret(
        fieldModulus: SECP256K1_CURVE_N,
        secret: BigUInt(privateKeyTweak.dataRepresentation),
        threshold: threshold,
        numberOfShares: UInt32(signingOperatorMap.count)
    )
    var pubkeySharesTweak: [String: Data] = [:]
    for (identifier, signingOperator) in signingOperatorMap {
        guard let share = shares[signingOperator.id + 1] else {
            throw SparkError(message: "Cannot find share for identifier: \(signingOperator.id))")
        }
        let privateKeyTweak = try secp256k1.Signing.PrivateKey(
            dataRepresentation: share.share.magnitude.serialize().padTo32Bytes()
        )
        pubkeySharesTweak[identifier] = privateKeyTweak.publicKey.dataRepresentation
    }

    // Compute secret cipher
    let secretCipher = try encryptEcies(
        msg: leaf.newSigningPrivateKey.dataRepresentation,
        publicKey: receiverIdentityPublicKey
    )
    let signingHash = try getLeafSigningMessageHash(
        transferId: transferId.uuidString,
        leafId: leaf.leafId,
        secretCipher: secretCipher
    )
    let signature = try identityPrivateKey.signature(for: signingHash)

    var leafKeyTweakMap: [String: Spark_SendLeafKeyTweak] = [:]
    for (identifier, signingOperator) in signingOperatorMap {
        guard let share = shares[signingOperator.id + 1] else {
            throw SparkError(message: "Cannot find share for identifier: \(signingOperator.id))")
        }
        var secretShareTweak = Spark_SecretShareTweak()
        secretShareTweak.tweak = share.share.serialize()
        secretShareTweak.proofs = share.proof

        var sendLeafKeyTweak = Spark_SendLeafKeyTweak()
        sendLeafKeyTweak.leafID = leaf.leafId
        sendLeafKeyTweak.secretShareTweak = secretShareTweak
        sendLeafKeyTweak.pubkeySharesTweak = pubkeySharesTweak
        sendLeafKeyTweak.secretCipher = secretCipher
        sendLeafKeyTweak.signature = signature.dataRepresentation

        leafKeyTweakMap[identifier] = sendLeafKeyTweak
    }
    return leafKeyTweakMap
}

private func getLeafSigningMessageHash(
    transferId: String,
    leafId: String,
    secretCipher: Data
) throws -> Data {
    // Generate signing message over Sha256(leaf_id||transfer_id||secret_cipher)
    guard let leafIdData = leafId.lowercased().data(using: .utf8),
        let transferIdData = transferId.lowercased().data(using: .utf8)
    else {
        throw SparkError(message: "Cannot encode leaf id or transfer id")
    }
    var message = Data()
    message.append(leafIdData)
    message.append(transferIdData)
    message.append(secretCipher)
    return Data(SHA256.hash(data: message))
}

func decryptPendingTransferLeavesSecrets(
    identityPrivateKey: secp256k1.Signing.PrivateKey,
    transfer: Spark_Transfer
) throws -> [String: secp256k1.Signing.PrivateKey] {
    var leafSecretMap: [String: secp256k1.Signing.PrivateKey] = [:]
    let senderPubkey = try secp256k1.Signing.PublicKey(
        dataRepresentation: transfer.senderIdentityPublicKey,
        format: secp256k1.Format.compressed
    )
    for leaf in transfer.leaves {
        let leafId = leaf.leaf.id
        let signingHash = try getLeafSigningMessageHash(
            transferId: transfer.id,
            leafId: leafId,
            secretCipher: leaf.secretCipher
        )
        let signature = try secp256k1.Signing.ECDSASignature(dataRepresentation: leaf.signature)
        if !senderPubkey.isValidSignature(signature, for: signingHash) {
            throw SparkError(message: "Cannot verify signature of leaf \(leafId))")
        }
        let secret = try decryptEcies(
            encryptedMsg: leaf.secretCipher,
            privateKey: identityPrivateKey.dataRepresentation
        )
        leafSecretMap[leafId] = try secp256k1.Signing.PrivateKey(dataRepresentation: secret)
    }
    return leafSecretMap
}

func claimTransfer(
    signingCoordinator: SigningOperator,
    signingOperatorMap: [String: SigningOperator],
    transfer: Spark_Transfer,
    leafKeyTweakMap: [String: LeafKeyTweak],
    identityPrivateKey: secp256k1.Signing.PrivateKey,
    threshold: UInt32
) async throws {
    try await claimTransferTweakKeys(
        signingOperatorMap: signingOperatorMap,
        transfer: transfer,
        leafKeyTweakMap: leafKeyTweakMap,
        identityPrivateKey: identityPrivateKey,
        threshold: threshold
    )
    let _ = try await claimTransferSignRefunds(
        signingCoordinator: signingCoordinator,
        transfer: transfer,
        leafKeyTweakMap: leafKeyTweakMap,
        identityPrivateKey: identityPrivateKey
    )
}

private func claimTransferTweakKeys(
    signingOperatorMap: [String: SigningOperator],
    transfer: Spark_Transfer,
    leafKeyTweakMap: [String: LeafKeyTweak],
    identityPrivateKey: secp256k1.Signing.PrivateKey,
    threshold: UInt32
) async throws {
    let leafKeyTweaksMap = try prepareClaimLeafKeyTweaks(
        signingOperatorMap: signingOperatorMap,
        leafKeyTweakMap: leafKeyTweakMap,
        threshold: threshold
    )
    for (identifier, signingOperator) in signingOperatorMap {
        guard let leafKeyTweaks = leafKeyTweaksMap[identifier] else {
            throw SparkError(message: "Cannot find leaf key tweaks to claim")
        }

        var request = Spark_ClaimTransferTweakKeysRequest()
        request.transferID = transfer.id
        request.ownerIdentityPublicKey = identityPrivateKey.publicKey.dataRepresentation
        request.leavesToReceive = leafKeyTweaks
        let _ = try await signingOperator.client.claim_transfer_tweak_keys(request)
    }
}

private func prepareClaimLeafKeyTweaks(
    signingOperatorMap: [String: SigningOperator],
    leafKeyTweakMap: [String: LeafKeyTweak],
    threshold: UInt32
) throws -> [String: [Spark_ClaimLeafKeyTweak]] {
    var leafKeyTweaksMap: [String: [Spark_ClaimLeafKeyTweak]] = [:]
    for leaf in leafKeyTweakMap.values {
        let leafKeyTweakMap = try prepareSingleClaimLeafKeyTweak(
            signingOperatorMap: signingOperatorMap,
            leaf: leaf,
            threshold: threshold
        )
        for (identifier, leafTweak) in leafKeyTweakMap {
            leafKeyTweaksMap[identifier, default: []].append(leafTweak)
        }
    }
    return leafKeyTweaksMap
}

private func prepareSingleClaimLeafKeyTweak(
    signingOperatorMap: [String: SigningOperator],
    leaf: LeafKeyTweak,
    threshold: UInt32
) throws -> [String: Spark_ClaimLeafKeyTweak] {
    let privateKeyTweak = try leaf.signingPrivateKey.subtract(leaf.newSigningPrivateKey)

    let shares = try splitSecret(
        fieldModulus: SECP256K1_CURVE_N,
        secret: BigUInt(privateKeyTweak.dataRepresentation),
        threshold: threshold,
        numberOfShares: UInt32(signingOperatorMap.count)
    )
    var pubkeySharesTweak: [String: Data] = [:]
    for (identifier, signingOperator) in signingOperatorMap {
        guard let share = shares[signingOperator.id + 1] else {
            throw SparkError(message: "Cannot find share for identifier: \(signingOperator.id))")
        }
        let privateKeyTweak = try secp256k1.Signing.PrivateKey(
            dataRepresentation: share.share.magnitude.serialize().padTo32Bytes()
        )
        pubkeySharesTweak[identifier] = privateKeyTweak.publicKey.dataRepresentation
    }

    var leafKeyTweakMap: [String: Spark_ClaimLeafKeyTweak] = [:]
    for (identifier, signingOperator) in signingOperatorMap {
        guard let share = shares[signingOperator.id + 1] else {
            throw SparkError(message: "Cannot find share for identifier: \(signingOperator.id))")
        }
        var secretShareTweak = Spark_SecretShareTweak()
        secretShareTweak.tweak = share.share.serialize()
        secretShareTweak.proofs = share.proof

        var claimLeafKeyTweak = Spark_ClaimLeafKeyTweak()
        claimLeafKeyTweak.leafID = leaf.leafId
        claimLeafKeyTweak.secretShareTweak = secretShareTweak
        claimLeafKeyTweak.pubkeySharesTweak = pubkeySharesTweak

        leafKeyTweakMap[identifier] = claimLeafKeyTweak
    }
    return leafKeyTweakMap
}

struct LeafTransferSigningData {
    let signingPrivateKey: secp256k1.Signing.PrivateKey
    let refundTx: TransactionResult
    let nonce: NonceResult
    let keyPackage: KeyPackage
}

private func claimTransferSignRefunds(
    signingCoordinator: SigningOperator,
    transfer: Spark_Transfer,
    leafKeyTweakMap: [String: LeafKeyTweak],
    identityPrivateKey: secp256k1.Signing.PrivateKey
) async throws -> [String: Data] {
    let (signingJobs, signingDataMap) = try prepareClaimTransferOperatorsSigningJobs(
        transfer: transfer,
        leafKeyTweakMap: leafKeyTweakMap
    )
    var request = Spark_ClaimTransferSignRefundsRequest()
    request.transferID = transfer.id
    request.ownerIdentityPublicKey = identityPrivateKey.publicKey.dataRepresentation
    request.signingJobs = signingJobs

    let response = try await signingCoordinator.client.claim_transfer_sign_refunds(request)
    return try signRefunds(
        operatorSigningResults: response.signingResults,
        signingDataMap: signingDataMap
    )
}

private func prepareClaimTransferOperatorsSigningJobs(
    transfer: Spark_Transfer,
    leafKeyTweakMap: [String: LeafKeyTweak]
) throws -> ([Spark_ClaimLeafSigningJob], [String: LeafTransferSigningData]) {
    var signingJobs: [Spark_ClaimLeafSigningJob] = []
    var signingDataMap: [String: LeafTransferSigningData] = [:]
    for transferLeaf in transfer.leaves {
        let leaf = transferLeaf.leaf
        guard let signingPrivateKey = leafKeyTweakMap[leaf.id]?.newSigningPrivateKey else {
            throw SparkError(message: "Cannot find leaf key tweaks")
        }
        // TODO: replace hardcoded network
        let refundTx = try constructRefundTx(
            tx: leaf.nodeTx,
            vout: 0,
            pubkey: signingPrivateKey.publicKey.dataRepresentation,
            network: "regtest",
            locktime: 60000
        )

        let keyPackage = KeyPackage(
            secretKey: signingPrivateKey.dataRepresentation,
            publicKey: signingPrivateKey.publicKey.dataRepresentation,
            verifyingKey: leaf.verifyingKey
        )
        let nonce = try frostNonce(keyPackage: keyPackage)

        var signingJob = Spark_ClaimLeafSigningJob()
        signingJob.leafID = leaf.id

        signingJob.refundTxSigningJob.signingPublicKey = signingPrivateKey.publicKey.dataRepresentation
        signingJob.refundTxSigningJob.rawTx = refundTx.tx
        signingJob.refundTxSigningJob.signingNonceCommitment.binding = nonce.commitment.binding
        signingJob.refundTxSigningJob.signingNonceCommitment.hiding = nonce.commitment.hiding

        signingJobs.append(signingJob)
        signingDataMap[leaf.id] = LeafTransferSigningData(
            signingPrivateKey: signingPrivateKey,
            refundTx: refundTx,
            nonce: nonce,
            keyPackage: keyPackage
        )
    }
    return (signingJobs, signingDataMap)
}

func signRefunds(
    operatorSigningResults: [Spark_ClaimLeafSigningResult],
    signingDataMap: [String: LeafTransferSigningData]
) throws -> [String: Data] {
    var signatureMap: [String: Data] = [:]
    for operatorSigningResult in operatorSigningResults {
        guard let signingData = signingDataMap[operatorSigningResult.leafID] else {
            throw SparkError(message: "Cannot find signing data for leaf ID \(operatorSigningResult.leafID)")
        }
        let operatorCommitments = operatorSigningResult.refundTxSigningResult.signingNonceCommitments.mapValues {
            value in
            SigningCommitment(hiding: value.hiding, binding: value.binding)
        }

        let selfSignature = try signFrost(
            msg: signingData.refundTx.sighash,
            keyPackage: signingData.keyPackage,
            nonce: signingData.nonce.nonce,
            selfCommitment: signingData.nonce.commitment,
            statechainCommitments: operatorCommitments
        )

        let aggregatedSignature = try aggregateFrost(
            msg: signingData.refundTx.sighash,
            statechainCommitments: operatorCommitments,
            selfCommitment: signingData.nonce.commitment,
            statechainSignatures: operatorSigningResult.refundTxSigningResult.signatureShares,
            selfSignature: selfSignature,
            statechainPublicKeys: operatorSigningResult.refundTxSigningResult.publicKeys,
            selfPublicKey: signingData.signingPrivateKey.publicKey.dataRepresentation,
            verifyingKey: signingData.keyPackage.verifyingKey
        )
        signatureMap[operatorSigningResult.leafID] = aggregatedSignature
    }
    return signatureMap
}
