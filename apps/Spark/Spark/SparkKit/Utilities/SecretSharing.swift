//
//  SecretSharing.swift
//  Spark
//
//  Created by Zhen Lu on 12/23/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import BigInt
import Foundation
import secp256k1

struct Polynomial {
    let fieldModulus: BigUInt
    let coefficients: [BigUInt]
    let proofs: [Data]

    init(fieldModulus: BigUInt, secret: BigUInt, degree: UInt32) throws {
        self.fieldModulus = fieldModulus
        var coefficients: [BigUInt] = []
        var proofs: [Data] = []
        coefficients.append(secret)
        proofs.append(
            try secp256k1.Signing.PrivateKey(dataRepresentation: secret.serialize().padTo32Bytes()).publicKey
                .dataRepresentation
        )

        for _ in 1..<degree {
            let coefficient = try secp256k1.Signing.PrivateKey()
            coefficients.append(BigUInt(coefficient.dataRepresentation))
            proofs.append(coefficient.publicKey.dataRepresentation)
        }
        self.coefficients = coefficients
        self.proofs = proofs
    }

    public func eval(_ x: BigUInt) throws -> BigUInt {
        var result = BigUInt(0)
        for (i, coef) in coefficients.enumerated() {
            result = (result + (coef * x.power(BigUInt(i), modulus: fieldModulus)) % fieldModulus) % fieldModulus
        }
        return result;
    }
}

public struct VerifiableSecretShare {
    let fieldModulus: BigUInt
    let threshold: UInt32
    let index: BigUInt
    let share: BigUInt
    let proof: [Data]
}

public func splitSecret(
    fieldModulus: BigUInt,
    secret: BigUInt,
    threshold: UInt32,
    numberOfShares: UInt32
) throws -> [VerifiableSecretShare] {
    let poly = try Polynomial(fieldModulus: fieldModulus, secret: secret, degree: threshold)
    var result: [VerifiableSecretShare] = []
    for i in 1...numberOfShares {
        let share = try poly.eval(BigUInt(i))
        result.append(
            VerifiableSecretShare(
                fieldModulus: fieldModulus,
                threshold: threshold,
                index: BigUInt(i),
                share: share,
                proof: poly.proofs
            )
        )
    }
    return result
}

func computeLagrangeCoefficients(index: BigUInt, shares: [VerifiableSecretShare]) throws -> BigUInt {
    var numerator = BigUInt(1)
    var denominator = BigUInt(1)
    guard let fieldModulus = shares.first?.fieldModulus else {
        throw SparkError(message: "Not enough shares")
    }
    for share in shares {
        if share.index == index {
            continue
        }
        numerator = (numerator * share.index) % share.fieldModulus
        denominator =
            (denominator * ((share.index + (share.fieldModulus - index)) % share.fieldModulus)) % share.fieldModulus
    }
    guard let inversion = denominator.inverse(fieldModulus) else {
        throw SparkError(message: "Error computing inverse")
    }
    return (numerator * inversion) % fieldModulus
}

public func recoverSecret(shares: [VerifiableSecretShare]) throws -> BigUInt {
    if shares.first?.threshold ?? UInt32.max > shares.count {
        throw SparkError(message: "Not enough shares")
    }

    var result = BigUInt(0)
    for share in shares {
        let coef = try computeLagrangeCoefficients(index: share.index, shares: shares)
        result = (result + coef * share.share) % share.fieldModulus
    }

    return result
}
