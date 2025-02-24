import { DummyTx, KeyPackage, SigningCommitment, SigningNonce, TransactionResult } from "../wasm/spark_bindings.js";
export type SignFrostParams = {
    msg: Uint8Array;
    keyPackage: KeyPackage;
    nonce: SigningNonce;
    selfCommitment: SigningCommitment;
    statechainCommitments: any;
    adaptorPubKey?: Uint8Array | undefined;
};
export type AggregateFrostParams = {
    msg: Uint8Array;
    statechainCommitments: any;
    selfCommitment: SigningCommitment;
    statechainSignatures: any;
    selfSignature: Uint8Array;
    statechainPublicKeys: any;
    selfPublicKey: Uint8Array;
    verifyingKey: Uint8Array;
    adaptorPubKey?: Uint8Array | undefined;
};
export type ConstructNodeTxParams = {
    tx: Uint8Array;
    vout: number;
    address: string;
    locktime: number;
};
export declare function signFrost({ msg, keyPackage, nonce, selfCommitment, statechainCommitments, adaptorPubKey, }: SignFrostParams): Uint8Array;
export declare function aggregateFrost({ msg, statechainCommitments, selfCommitment, statechainSignatures, selfSignature, statechainPublicKeys, selfPublicKey, verifyingKey, adaptorPubKey, }: AggregateFrostParams): Uint8Array;
export declare function constructNodeTx({ tx, vout, address, locktime, }: ConstructNodeTxParams): TransactionResult;
export declare function constructRefundTx({ tx, vout, pubkey, network, locktime, }: {
    tx: Uint8Array;
    vout: number;
    pubkey: Uint8Array;
    network: string;
    locktime: number;
}): TransactionResult;
export declare function constructSplitTx({ tx, vout, addresses, locktime, }: {
    tx: Uint8Array;
    vout: number;
    addresses: string[];
    locktime: number;
}): TransactionResult;
export declare function createDummyTx({ address, amountSats, }: {
    address: string;
    amountSats: bigint;
}): DummyTx;
export declare function encryptEcies({ msg, publicKey, }: {
    msg: Uint8Array;
    publicKey: Uint8Array;
}): Uint8Array;
export declare function decryptEcies({ encryptedMsg, privateKey, }: {
    encryptedMsg: Uint8Array;
    privateKey: Uint8Array;
}): Uint8Array;
