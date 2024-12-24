//
//  Wallet.swift
//  Spark
//
//  Created by Zhen Lu on 12/20/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import Foundation
import secp256k1

public class Wallet {
    let walletClient: Spark_SparkServiceAsyncClient

    let identityPrivateKey: secp256k1.Signing.PrivateKey

    var addressKeyMap: [String: secp256k1.Signing.PrivateKey] = [:]
    var nodeIDKeyMap: [String: secp256k1.Signing.PrivateKey] = [:]

    public init(walletClient: Spark_SparkServiceAsyncClient) throws {
        self.walletClient = walletClient
        self.identityPrivateKey = try secp256k1.Signing.PrivateKey()
    }

    public func generateDepositAddress() async throws -> Spark_Address {
        let signingKey = try secp256k1.Signing.PrivateKey()
        let address = try await Spark.generateDepositAddress(
            client: self.walletClient,
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
            client: self.walletClient,
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
}
