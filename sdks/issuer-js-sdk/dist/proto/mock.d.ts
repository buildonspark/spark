import { BinaryReader, BinaryWriter } from "@bufbuild/protobuf/wire";
import { type CallContext, type CallOptions } from "nice-grpc-common";
import { Empty } from "./google/protobuf/empty.js";
export declare const protobufPackage = "mock";
export interface CleanUpPreimageShareRequest {
    paymentHash: Uint8Array;
}
export declare const CleanUpPreimageShareRequest: MessageFns<CleanUpPreimageShareRequest>;
export type MockServiceDefinition = typeof MockServiceDefinition;
export declare const MockServiceDefinition: {
    readonly name: "MockService";
    readonly fullName: "mock.MockService";
    readonly methods: {
        readonly clean_up_preimage_share: {
            readonly name: "clean_up_preimage_share";
            readonly requestType: MessageFns<CleanUpPreimageShareRequest>;
            readonly requestStream: false;
            readonly responseType: import("./google/protobuf/empty.js").MessageFns<Empty>;
            readonly responseStream: false;
            readonly options: {};
        };
    };
};
export interface MockServiceImplementation<CallContextExt = {}> {
    clean_up_preimage_share(request: CleanUpPreimageShareRequest, context: CallContext & CallContextExt): Promise<DeepPartial<Empty>>;
}
export interface MockServiceClient<CallOptionsExt = {}> {
    clean_up_preimage_share(request: DeepPartial<CleanUpPreimageShareRequest>, options?: CallOptions & CallOptionsExt): Promise<Empty>;
}
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
