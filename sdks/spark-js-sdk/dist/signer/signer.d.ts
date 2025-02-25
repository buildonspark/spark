import { TreeNode } from "../proto/spark.js";
import { VerifiableSecretShare } from "../utils/secret-sharing.js";
export type SigningNonce = {
    binding: Uint8Array;
    hiding: Uint8Array;
};
export type SigningCommitment = {
    binding: Uint8Array;
    hiding: Uint8Array;
};
export type SignFrostParams = {
    message: Uint8Array;
    privateAsPubKey: Uint8Array;
    publicKey: Uint8Array;
    verifyingKey: Uint8Array;
    selfCommitment: SigningCommitment;
    statechainCommitments?: {
        [key: string]: SigningCommitment;
    } | undefined;
    adaptorPubKey?: Uint8Array | undefined;
};
export type AggregateFrostParams = Omit<SignFrostParams, "privateAsPubKey"> & {
    selfSignature: Uint8Array;
    statechainSignatures?: {
        [key: string]: Uint8Array;
    } | undefined;
    statechainPublicKeys?: {
        [key: string]: Uint8Array;
    } | undefined;
};
export type SplitSecretWithProofsParams = {
    secret: Uint8Array;
    curveOrder: bigint;
    threshold: number;
    numShares: number;
    isSecretPubkey?: boolean;
};
interface SparkSigner {
    getIdentityPublicKey(): Promise<Uint8Array>;
    generateMnemonic(): Promise<string>;
    createSparkWalletFromMnemonic(mnemonic: string): Promise<string>;
    createSparkWalletFromSeed(seed: Uint8Array | string): Promise<string>;
    restoreSigningKeysFromLeafs(leafs: TreeNode[]): Promise<void>;
    getTrackedPublicKeys(): Promise<Uint8Array[]>;
    generatePublicKey(hash?: Uint8Array): Promise<Uint8Array>;
    removePublicKey(publicKey: Uint8Array): Promise<void>;
    getSchnorrPublicKey(publicKey: Uint8Array): Promise<Uint8Array>;
    signSchnorr(message: Uint8Array, publicKey: Uint8Array): Promise<Uint8Array>;
    subtractPrivateKeysGivenPublicKeys(first: Uint8Array, second: Uint8Array): Promise<Uint8Array>;
    splitSecretWithProofs(params: SplitSecretWithProofsParams): Promise<VerifiableSecretShare[]>;
    signFrost(params: SignFrostParams): Promise<Uint8Array>;
    aggregateFrost(params: AggregateFrostParams): Promise<Uint8Array>;
    signMessageWithPublicKey(message: Uint8Array, publicKey: Uint8Array, compact?: boolean): Promise<Uint8Array>;
    signMessageWithIdentityKey(message: Uint8Array, compact?: boolean): Promise<Uint8Array>;
    encryptLeafPrivateKeyEcies(receiverPublicKey: Uint8Array, publicKey: Uint8Array): Promise<Uint8Array>;
    decryptEcies(ciphertext: Uint8Array): Promise<Uint8Array>;
    getRandomSigningCommitment(): Promise<SigningCommitment>;
    getSspIdentityPublicKey(): Promise<Uint8Array>;
    hashRandomPrivateKey(): Promise<Uint8Array>;
    generateAdaptorFromSignature(signature: Uint8Array): Promise<{
        adaptorSignature: Uint8Array;
        adaptorPublicKey: Uint8Array;
    }>;
}
declare class DefaultSparkSigner implements SparkSigner {
    private identityPrivateKey;
    private publicKeyToPrivateKeyMap;
    private commitmentToNonceMap;
    private deriveSigningKey;
    restoreSigningKeysFromLeafs(leafs: TreeNode[]): Promise<void>;
    getSchnorrPublicKey(publicKey: Uint8Array): Promise<Uint8Array>;
    signSchnorr(message: Uint8Array, publicKey: Uint8Array): Promise<Uint8Array>;
    getIdentityPublicKey(): Promise<Uint8Array>;
    generateMnemonic(): Promise<string>;
    createSparkWalletFromMnemonic(mnemonic: string): Promise<string>;
    getTrackedPublicKeys(): Promise<Uint8Array[]>;
    generatePublicKey(hash?: Uint8Array): Promise<Uint8Array>;
    removePublicKey(publicKey: Uint8Array): Promise<void>;
    subtractPrivateKeysGivenPublicKeys(first: Uint8Array, second: Uint8Array): Promise<Uint8Array>;
    splitSecretWithProofs({ secret, curveOrder, threshold, numShares, isSecretPubkey, }: SplitSecretWithProofsParams): Promise<VerifiableSecretShare[]>;
    signFrost({ message, privateAsPubKey, publicKey, verifyingKey, selfCommitment, statechainCommitments, adaptorPubKey, }: SignFrostParams): Promise<Uint8Array>;
    aggregateFrost({ message, publicKey, verifyingKey, selfCommitment, statechainCommitments, adaptorPubKey, selfSignature, statechainSignatures, statechainPublicKeys, }: AggregateFrostParams): Promise<Uint8Array>;
    createSparkWalletFromSeed(seed: Uint8Array): Promise<string>;
    signMessageWithPublicKey(message: Uint8Array, publicKey: Uint8Array, compact?: boolean): Promise<Uint8Array>;
    signMessageWithIdentityKey(message: Uint8Array, compact?: boolean): Promise<Uint8Array>;
    encryptLeafPrivateKeyEcies(receiverPublicKey: Uint8Array, publicKey: Uint8Array): Promise<Uint8Array>;
    decryptEcies(ciphertext: Uint8Array): Promise<Uint8Array>;
    getRandomSigningCommitment(): Promise<SigningCommitment>;
    getSspIdentityPublicKey(): Promise<Uint8Array>;
    hashRandomPrivateKey(): Promise<Uint8Array>;
    generateAdaptorFromSignature(signature: Uint8Array): Promise<{
        adaptorSignature: Uint8Array;
        adaptorPublicKey: Uint8Array;
    }>;
}
export { DefaultSparkSigner };
export type { SparkSigner };
