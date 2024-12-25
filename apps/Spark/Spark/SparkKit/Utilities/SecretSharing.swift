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
    let fieldModulus: BigInt
    let coefficients: [BigInt]
    let proofs: [Data]

    init(fieldModulus: BigInt, secret: BigInt, degree: UInt32) throws {
        self.fieldModulus = fieldModulus
        var coefficients: [BigInt] = []
        var proofs: [Data] = []
        coefficients.append(secret)
        proofs.append(
            try secp256k1.Signing.PrivateKey(dataRepresentation: secret.serialize().padTo32Bytes()).publicKey
                .dataRepresentation
        )

        for _ in 1..<degree {
            let coefficient = try secp256k1.Signing.PrivateKey()
            let mag = BigUInt(coefficient.dataRepresentation)
            coefficients.append(BigInt(sign: .plus, magnitude: mag))
            proofs.append(coefficient.publicKey.dataRepresentation)
        }
        self.coefficients = coefficients
        self.proofs = proofs
    }

    public func eval(_ x: BigInt) throws -> BigInt {
        var result = BigInt(0)
        for (i, coef) in coefficients.enumerated() {
            result = (result + (coef * x.power(BigInt(i), modulus: fieldModulus)) % fieldModulus) % fieldModulus
        }
        return result;
    }
}

public struct VerifiableSecretShare {
    let fieldModulus: BigInt
    let threshold: UInt32
    let index: BigInt
    let share: BigInt
    let proof: [Data]
}

public func splitSecret(
    fieldModulus: BigInt,
    secret: BigInt,
    threshold: UInt32,
    numberOfShares: UInt32
) throws -> [VerifiableSecretShare] {
    let poly = try Polynomial(fieldModulus: fieldModulus, secret: secret, degree: threshold)
    var result: [VerifiableSecretShare] = []
    for i in 1...numberOfShares {
        let share = try poly.eval(BigInt(i))
        result.append(
            VerifiableSecretShare(
                fieldModulus: fieldModulus,
                threshold: threshold,
                index: BigInt(i),
                share: share,
                proof: poly.proofs
            )
        )
    }
    return result
}

func computeLagrangeCoefficients(index: BigInt, shares: [VerifiableSecretShare]) throws -> BigInt {
    var numerator = BigInt(1)
    var denominator = BigInt(1)
    guard let fieldModulus = shares.first?.fieldModulus else {
        throw SparkError(message: "Not enough shares")
    }
    for share in shares {
        if share.index == index {
            continue
        }
        numerator = (numerator * share.index) % share.fieldModulus
        denominator =
            (denominator * ((share.index - index + share.fieldModulus) % share.fieldModulus)) % share.fieldModulus
    }
    guard let inversion = denominator.inverse(fieldModulus) else {
        throw SparkError(message: "Error computing inverse")
    }
    return (numerator * inversion) % fieldModulus
}

public func recoverSecret(shares: [VerifiableSecretShare]) throws -> BigInt {
    if shares.first?.threshold ?? UInt32.max > shares.count {
        throw SparkError(message: "Not enough shares")
    }

    var result = BigInt(0)
    for share in shares {
        let coef = try computeLagrangeCoefficients(index: share.index, shares: shares)
        result = (result + coef * share.share) % share.fieldModulus
    }

    return result
}
