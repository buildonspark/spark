//
//  SecretSharingTests.swift
//  Spark
//
//  Created by Zhen Lu on 12/23/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//


import Testing
import Spark
import BigInt

struct SecretSharingTests {
    @Test func testSecretSharing() throws {
        let shares = try Spark.splitSecret(fieldModulus: SECP256K1_CURVE_N, secret: BigInt(100), threshold: 3, numberOfShares: 5)
        #expect(shares.count == 5)

        let recovered = try Spark.recoverSecret(shares: shares)
        #expect(recovered == BigInt(100))
    }
}
