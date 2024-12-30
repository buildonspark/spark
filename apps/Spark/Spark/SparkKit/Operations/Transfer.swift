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
        let leafTweaksMap = try prepareSingleSendLeafKeyTweak(
            signingOperatorMap: signingOperatorMap,
            transferId: transferId,
            leaf: leaf,
            receiverIdentityPublicKey: receiverIdentityPublicKey,
            identityPrivateKey: identityPrivateKey,
            threshold: threshold
        )
        for (identifier, leafTweak) in leafTweaksMap {
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

    var leafTweaksMap: [String: Spark_SendLeafKeyTweak] = [:]
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

        leafTweaksMap[identifier] = sendLeafKeyTweak
    }
    return leafTweaksMap
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
) throws -> [String: Data] {
    var leafSecretMap: [String: Data] = [:]
    let senderIdentityPublicKey = transfer.senderIdentityPublicKey
    let senderPubkey = try secp256k1.Signing.PublicKey(
        dataRepresentation: transfer.senderIdentityPublicKey,
        format: secp256k1.Format.compressed
    )
    for leaf in transfer.leaves {
        let signingHash = try getLeafSigningMessageHash(
            transferId: transfer.id,
            leafId: leaf.leafID,
            secretCipher: leaf.secretCipher
        )
        let signature = try secp256k1.Signing.ECDSASignature(dataRepresentation: leaf.signature)
        if !senderPubkey.isValidSignature(signature, for: signingHash) {
            throw SparkError(message: "Cannot verify signature of leaf \(leaf.leafID))")
        }
        let secret = try decryptEcies(
            encryptedMsg: leaf.secretCipher,
            privateKey: identityPrivateKey.dataRepresentation
        )
        leafSecretMap[leaf.leafID] = secret
    }
    return leafSecretMap
}
