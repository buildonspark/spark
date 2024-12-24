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

public let SECP256K1_CURVE_N = BigInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", radix: 16)!
public let SECP256K1_CURVE_P = BigInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", radix: 16)!

extension secp256k1.Signing.PrivateKey {
    public func subtract(_ other: secp256k1.Signing.PrivateKey) throws -> secp256k1.Signing.PrivateKey {
        let aMag = BigUInt(self.dataRepresentation)
        let bMag = BigUInt(other.dataRepresentation)
        let resultInt =
            (BigInt(sign: .plus, magnitude: aMag) - BigInt(sign: .plus, magnitude: bMag) + SECP256K1_CURVE_N)
            & SECP256K1_CURVE_N;
        return try secp256k1.Signing.PrivateKey(dataRepresentation: resultInt.magnitude.serialize().padTo32Bytes())
    }
}
