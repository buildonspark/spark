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
                if !compareTransfers(existingTransfer, response.transfer) {
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

private func compareTransfers(_ transfer1: Spark_Transfer, _ transfer2: Spark_Transfer) -> Bool {
    return transfer1.id == transfer2.id && transfer1.senderIdentityPublicKey == transfer2.senderIdentityPublicKey
        && transfer1.receiverIdentityPublicKey == transfer2.receiverIdentityPublicKey
        && transfer1.status == transfer2.status && transfer1.totalValue == transfer2.totalValue
        && transfer1.expiryTime == transfer2.expiryTime && compareTransferLeaves(transfer1.leaves, transfer2.leaves)
}

private func compareTransferLeaves(_ leaves1: [Spark_TransferLeaf], _ leaves2: [Spark_TransferLeaf]) -> Bool {
    if leaves1.count != leaves2.count {
        return false
    }
    for (leaf1, leaf2) in zip(leaves1, leaves2) {
        if leaf1.leafID != leaf2.leafID || leaf1.secretCipher != leaf2.secretCipher
            || leaf1.signature != leaf2.signature || leaf1.rawTx != leaf2.rawTx
        {
            return false
        }
    }
    return true
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
        let share = try findShare(shares: shares, operatorId: signingOperator.id)
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

    // Generate signature over Sha256(leaf_id||transfer_id||secret_cipher)
    guard let leafIdData = leaf.leafId.data(using: .utf8),
        let transferIdData = transferId.uuidString.data(using: .utf8)
    else {
        throw SparkError(message: "Cannot encode leaf id or transfer id")
    }
    var message = Data()
    message.append(leafIdData)
    message.append(transferIdData)
    message.append(secretCipher)
    let digest = SHA256.hash(data: message)
    let signature = try identityPrivateKey.signature(for: Data(digest))

    var leafTweaksMap: [String: Spark_SendLeafKeyTweak] = [:]
    for (identifier, signingOperator) in signingOperatorMap {
        let share = try findShare(shares: shares, operatorId: signingOperator.id)
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

private func findShare(shares: [VerifiableSecretShare], operatorId: UInt32) throws -> VerifiableSecretShare {
    let targetShareIndex = BigInt(operatorId + 1)
    guard let share = shares.first(where: { $0.index == targetShareIndex }) else {
        throw SparkError(message: "Cannot find share")
    }
    return share
}
