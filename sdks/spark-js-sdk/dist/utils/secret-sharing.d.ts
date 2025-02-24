type Polynomial = {
    fieldModulus: bigint;
    coefficients: bigint[];
    proofs: Uint8Array[];
};
type SecretShare = {
    fieldModulus: bigint;
    threshold: number;
    index: bigint;
    share: bigint;
};
export type VerifiableSecretShare = SecretShare & {
    proofs: Uint8Array[];
};
export declare function getRandomBigInt(max: bigint): bigint;
export declare function modInverse(a: bigint, m: bigint): bigint;
export declare function evaluatePolynomial(polynomial: Polynomial, x: bigint): bigint;
export declare function fieldDiv(numerator: bigint, denominator: bigint, fieldModulus: bigint): bigint;
export declare function computerLagrangeCoefficients(index: bigint, points: SecretShare[]): bigint;
export declare function generatePolynomialForSecretSharing(fieldModulus: bigint, secret: bigint, degree: number): Polynomial;
export declare function splitSecret(fieldModulus: bigint, secret: bigint, threshold: number, numberOfShares: number): SecretShare[];
export declare function splitSecretWithProofs(secret: bigint, fieldModulus: bigint, threshold: number, numberOfShares: number): VerifiableSecretShare[];
export declare function recoverSecret(shares: VerifiableSecretShare[]): bigint;
export declare function validateShare(share: VerifiableSecretShare): void;
export declare function bigIntToPrivateKey(value: bigint): Uint8Array;
export {};
