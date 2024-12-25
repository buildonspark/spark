//
//  Keys.swift
//  Spark
//
//  Created by Zhen Lu on 12/23/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import BigInt
import Foundation
import secp256k1

public let SECP256K1_CURVE_N = BigUInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", radix: 16)!
public let SECP256K1_CURVE_P = BigUInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", radix: 16)!

extension secp256k1.Signing.PrivateKey {
    public func subtract(_ other: secp256k1.Signing.PrivateKey) throws -> secp256k1.Signing.PrivateKey {
        let resultInt =
            (BigUInt(self.dataRepresentation) + SECP256K1_CURVE_N - BigUInt(other.dataRepresentation))
            & SECP256K1_CURVE_N;
        return try secp256k1.Signing.PrivateKey(dataRepresentation: resultInt.magnitude.serialize().padTo32Bytes())
    }
}
