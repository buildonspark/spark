//
//  Wallet.swift
//  Spark
//
//  Created by Zhen Lu on 12/20/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import secp256k1

class Wallet {
    let walletClient: Spark_SparkServiceAsyncClient

    init(walletClient: Spark_SparkServiceAsyncClient) {
        self.walletClient = walletClient
    }

    public func generateDepositAddress() async throws -> Spark_Address {
        let identityPubkey = try secp256k1.Signing.PrivateKey().publicKey.dataRepresentation;
        let signingPubkey = try secp256k1.Signing.PrivateKey().publicKey.dataRepresentation;
        return try await Spark.generateDepositAddress(client: self.walletClient, identityPublicKey: identityPubkey, signingPublicKey: signingPubkey)
    }
}
