export declare function addPublicKeys(a: Uint8Array, b: Uint8Array): Uint8Array;
export declare function applyAdditiveTweakToPublicKey(pubkey: Uint8Array, tweak: Uint8Array): Uint8Array<ArrayBufferLike>;
export declare function subtractPublicKeys(a: Uint8Array, b: Uint8Array): Uint8Array<ArrayBufferLike>;
export declare function addPrivateKeys(a: Uint8Array, b: Uint8Array): Uint8Array<ArrayBufferLike>;
export declare function subtractPrivateKeys(a: Uint8Array, b: Uint8Array): Uint8Array<ArrayBufferLike>;
export declare function sumOfPrivateKeys(keys: Uint8Array[]): Uint8Array<ArrayBufferLike>;
export declare function lastKeyWithTarget(target: Uint8Array, keys: Uint8Array[]): Uint8Array<ArrayBufferLike>;
