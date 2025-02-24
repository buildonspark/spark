export interface KeyshareWithOperatorIndex {
    index: number;
    keyshare: Uint8Array;
}
export declare function recoverPrivateKeyFromKeyshares(keyshares: KeyshareWithOperatorIndex[], threshold: number): Uint8Array;
