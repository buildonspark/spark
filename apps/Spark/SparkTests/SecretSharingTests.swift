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
        for i in 1...1000 {
            let shares = try Spark.splitSecret(fieldModulus: SECP256K1_CURVE_N, secret: BigUInt(i), threshold: 3, numberOfShares: 5)
            #expect(shares.count == 5)

            let recovered = try Spark.recoverSecret(shares: shares)
            #expect(recovered == BigInt(i))
        }
    }
}
