export declare function generateSignatureFromExistingAdaptor(signature: Uint8Array, adaptorPrivateKeyBytes: Uint8Array): Uint8Array;
export declare function generateAdaptorFromSignature(signature: Uint8Array): {
    adaptorSignature: Uint8Array;
    adaptorPrivateKey: Uint8Array;
};
export declare function validateOutboundAdaptorSignature(pubkey: Uint8Array, hash: Uint8Array, signature: Uint8Array, adaptorPubkey: Uint8Array): boolean;
export declare function applyAdaptorToSignature(pubkey: Uint8Array, hash: Uint8Array, signature: Uint8Array, adaptorPrivateKeyBytes: Uint8Array): Uint8Array;
