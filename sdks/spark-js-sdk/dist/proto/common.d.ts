import { BinaryReader, BinaryWriter } from "@bufbuild/protobuf/wire";
export declare const protobufPackage = "common";
export declare enum SignatureIntent {
    CREATION = 0,
    TRANSFER = 1,
    AGGREGATE = 2,
    REFRESH = 3,
    UNRECOGNIZED = -1
}
export declare function signatureIntentFromJSON(object: any): SignatureIntent;
export declare function signatureIntentToJSON(object: SignatureIntent): string;
/** A map from a string to a bytes. It's a workaround to have map arrays in proto. */
export interface PackageMap {
    packages: {
        [key: string]: Uint8Array;
    };
}
export interface PackageMap_PackagesEntry {
    key: string;
    value: Uint8Array;
}
/**
 * A commitment for frost signing.
 * It's a pair of public keys (points) in secp256k1 curve.
 */
export interface SigningCommitment {
    /** The public key for hiding. 33 bytes. */
    hiding: Uint8Array;
    /** The public key for binding. 33 bytes. */
    binding: Uint8Array;
}
export interface SigningResult {
    signatureShare: Uint8Array;
}
export declare const PackageMap: MessageFns<PackageMap>;
export declare const PackageMap_PackagesEntry: MessageFns<PackageMap_PackagesEntry>;
export declare const SigningCommitment: MessageFns<SigningCommitment>;
export declare const SigningResult: MessageFns<SigningResult>;
type Builtin = Date | Function | Uint8Array | string | number | boolean | undefined;
export type DeepPartial<T> = T extends Builtin ? T : T extends globalThis.Array<infer U> ? globalThis.Array<DeepPartial<U>> : T extends ReadonlyArray<infer U> ? ReadonlyArray<DeepPartial<U>> : T extends {
    $case: string;
} ? {
    [K in keyof Omit<T, "$case">]?: DeepPartial<T[K]>;
} & {
    $case: T["$case"];
} : T extends {} ? {
    [K in keyof T]?: DeepPartial<T[K]>;
} : Partial<T>;
export interface MessageFns<T> {
    encode(message: T, writer?: BinaryWriter): BinaryWriter;
    decode(input: BinaryReader | Uint8Array, length?: number): T;
    fromJSON(object: any): T;
    toJSON(message: T): unknown;
    create(base?: DeepPartial<T>): T;
    fromPartial(object: DeepPartial<T>): T;
}
export {};
