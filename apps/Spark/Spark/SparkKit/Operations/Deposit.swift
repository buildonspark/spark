//
//  Deposit.swift
//  Spark
//
//  Created by Zhen Lu on 12/18/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import secp256k1
import Foundation

func generateDepositAddress(
    client: Spark_SparkServiceAsyncClient,
    identityPublicKey: Data,
    signingPublicKey: Data
) async throws -> Spark_Address {
    var request = Spark_GenerateDepositAddressRequest()
    request.identityPublicKey = identityPublicKey;
    request.signingPublicKey = signingPublicKey;
    let response = try await client.generate_deposit_address(request)
    return response.depositAddress;
}

//func createTree(
//    onchainTx: Transaction,
//    vout: Int,
//    signingPrivateKey: Data,
//    identityPublicKey: Data,
//    verifyingPublicKey: Data
//) async throws -> Spark_FinalizeNodeSignaturesResponse {
//
//}
