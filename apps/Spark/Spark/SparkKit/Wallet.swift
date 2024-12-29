//
//  Wallet.swift
//  Spark
//
//  Created by Zhen Lu on 12/20/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import Foundation
import GRPC
import secp256k1

public class SigningOperator {
    let id: UInt32
    let identifier: String
    let client: Spark_SparkServiceAsyncClient

    public init(operatorId: UInt32, identifier: String) throws {
        self.id = operatorId
        self.identifier = identifier
        let eventLoopGroup = PlatformSupport.makeEventLoopGroup(loopCount: 1)
        let channel = try GRPCChannelPool.with(
            target: .host("localhost", port: 8535 + Int(operatorId)),
            transportSecurity: .plaintext,
            eventLoopGroup: eventLoopGroup
        )
        self.client = Spark_SparkServiceAsyncClient(channel: channel)
    }
}

public class Wallet {
    let signingOperatorMap: [String: SigningOperator]
    var coordinator: SigningOperator
    let identityPrivateKey: secp256k1.Signing.PrivateKey
    let threshold: uint32

    var addressKeyMap: [String: secp256k1.Signing.PrivateKey] = [:]
    var nodeIDKeyMap: [String: secp256k1.Signing.PrivateKey] = [:]

    public init(signingOperators: [SigningOperator]) throws {
        self.signingOperatorMap = Dictionary(uniqueKeysWithValues: signingOperators.map { ($0.identifier, $0) })
        self.identityPrivateKey = try secp256k1.Signing.PrivateKey()
        self.coordinator = signingOperators[0]
        self.threshold = 3
    }

    public func getIdentityPublicKey() -> secp256k1.Signing.PublicKey {
        return self.identityPrivateKey.publicKey
    }

    public func generateDepositAddress() async throws -> Spark_Address {
        let signingKey = try secp256k1.Signing.PrivateKey()
        let address = try await Spark.generateDepositAddress(
            client: self.coordinator.client,
            identityPublicKey: self.identityPrivateKey.publicKey.dataRepresentation,
            signingPublicKey: signingKey.publicKey.dataRepresentation
        )
        self.addressKeyMap[address.address] = signingKey
        return address
    }

    public func createTree(
        onchainTx: Data,
        onchainTxId: String,
        vout: UInt32,
        address: Spark_Address,
        network: String
    ) async throws -> Spark_FinalizeNodeSignaturesResponse {
        guard let signingPrivateKey = self.addressKeyMap[address.address] else {
            throw SparkError(message: "Invalid address")
        }
        let response = try await Spark.createTree(
            client: self.coordinator.client,
            onchainTx: onchainTx,
            onchainTxId: onchainTxId,
            vout: vout,
            address: address.address,
            network: network,
            signingPrivateKey: signingPrivateKey.dataRepresentation,
            identityPublicKey: self.identityPrivateKey.publicKey.dataRepresentation,
            verifyingPublicKey: address.verifyingKey
        )

        for node in response.nodes {
            self.nodeIDKeyMap[node.id] = signingPrivateKey
        }
        return response
    }

    public func sendTransfer(
        receiverIdentityPublicKey: Data,
        leafIds: [String],
        expiryTime: Date
    ) async throws -> Spark_Transfer {
        var leafKeyTweaks: [LeafKeyTweak] = []
        for leafId in leafIds {
            guard let signingPrivateKey = self.nodeIDKeyMap[leafId] else {
                throw SparkError(message: "Invalid leaf id " + leafId)
            }
            let newSigningPrivateKey = try secp256k1.Signing.PrivateKey()
            leafKeyTweaks.append(
                LeafKeyTweak(
                    leafId: leafId,
                    signingPrivateKey: signingPrivateKey,
                    newSigningPrivateKey: newSigningPrivateKey
                )
            )
        }
        let transfer = try await Spark.sendTransfer(
            signingOperatorMap: self.signingOperatorMap,
            leaves: leafKeyTweaks,
            expiryTime: expiryTime,
            receiverIdentityPublicKey: receiverIdentityPublicKey,
            identityPrivateKey: self.identityPrivateKey,
            threshold: self.threshold
        )
        for leafKeyTweak in leafKeyTweaks {
            self.nodeIDKeyMap[leafKeyTweak.leafId] = leafKeyTweak.newSigningPrivateKey
        }
        return transfer
    }

    public func queryPendingTransfers() async throws -> [Spark_Transfer] {
        var request = Spark_QueryPendingTransfersRequest()
        request.receiverIdentityPublicKey = self.identityPrivateKey.publicKey.dataRepresentation
        let response = try await self.coordinator.client.query_pending_transfers(request)
        return response.transfers
    }
}
