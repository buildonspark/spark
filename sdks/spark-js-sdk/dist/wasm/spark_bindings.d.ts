/**
 * @param {KeyPackage} key_package
 * @returns {NonceResult}
 */
export function frost_nonce(key_package: KeyPackage): NonceResult;
/**
 * @param {Uint8Array} msg
 * @param {KeyPackage} key_package
 * @param {SigningNonce} nonce
 * @param {SigningCommitment} self_commitment
 * @param {any} statechain_commitments
 * @param {Uint8Array | null} [adaptor_public_key]
 * @returns {Uint8Array}
 */
export function wasm_sign_frost(msg: Uint8Array, key_package: KeyPackage, nonce: SigningNonce, self_commitment: SigningCommitment, statechain_commitments: any, adaptor_public_key?: Uint8Array | null): Uint8Array;
/**
 * @param {Uint8Array} msg
 * @param {any} statechain_commitments
 * @param {SigningCommitment} self_commitment
 * @param {any} statechain_signatures
 * @param {Uint8Array} self_signature
 * @param {any} statechain_public_keys
 * @param {Uint8Array} self_public_key
 * @param {Uint8Array} verifying_key
 * @param {Uint8Array | null} [adaptor_public_key]
 * @returns {Uint8Array}
 */
export function wasm_aggregate_frost(msg: Uint8Array, statechain_commitments: any, self_commitment: SigningCommitment, statechain_signatures: any, self_signature: Uint8Array, statechain_public_keys: any, self_public_key: Uint8Array, verifying_key: Uint8Array, adaptor_public_key?: Uint8Array | null): Uint8Array;
/**
 * @param {Uint8Array} tx
 * @param {number} vout
 * @param {string} address
 * @param {number} locktime
 * @returns {TransactionResult}
 */
export function construct_node_tx(tx: Uint8Array, vout: number, address: string, locktime: number): TransactionResult;
/**
 * @param {Uint8Array} tx
 * @param {number} vout
 * @param {Uint8Array} pubkey
 * @param {string} network
 * @param {number} locktime
 * @returns {TransactionResult}
 */
export function construct_refund_tx(tx: Uint8Array, vout: number, pubkey: Uint8Array, network: string, locktime: number): TransactionResult;
/**
 * @param {Uint8Array} tx
 * @param {number} vout
 * @param {string[]} addresses
 * @param {number} locktime
 * @returns {TransactionResult}
 */
export function construct_split_tx(tx: Uint8Array, vout: number, addresses: string[], locktime: number): TransactionResult;
/**
 * @param {string} address
 * @param {bigint} amount_sats
 * @returns {DummyTx}
 */
export function create_dummy_tx(address: string, amount_sats: bigint): DummyTx;
/**
 * @param {Uint8Array} msg
 * @param {Uint8Array} public_key_bytes
 * @returns {Uint8Array}
 */
export function encrypt_ecies(msg: Uint8Array, public_key_bytes: Uint8Array): Uint8Array;
/**
 * @param {Uint8Array} encrypted_msg
 * @param {Uint8Array} private_key_bytes
 * @returns {Uint8Array}
 */
export function decrypt_ecies(encrypted_msg: Uint8Array, private_key_bytes: Uint8Array): Uint8Array;
export class DummyTx {
    static __wrap(ptr: any): any;
    __destroy_into_raw(): number | undefined;
    __wbg_ptr: number | undefined;
    free(): void;
    /**
     * @param {Uint8Array} arg0
     */
    set tx(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get tx(): Uint8Array;
    /**
     * @param {string} arg0
     */
    set txid(arg0: string);
    /**
     * @returns {string}
     */
    get txid(): string;
}
export class KeyPackage {
    /**
     * @param {Uint8Array} secret_key
     * @param {Uint8Array} public_key
     * @param {Uint8Array} verifying_key
     */
    constructor(secret_key: Uint8Array, public_key: Uint8Array, verifying_key: Uint8Array);
    __destroy_into_raw(): number;
    __wbg_ptr: number;
    free(): void;
    /**
     * @param {Uint8Array} arg0
     */
    set secret_key(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get secret_key(): Uint8Array;
    /**
     * @param {Uint8Array} arg0
     */
    set public_key(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get public_key(): Uint8Array;
    /**
     * @param {Uint8Array} arg0
     */
    set verifying_key(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get verifying_key(): Uint8Array;
}
export class NonceResult {
    static __wrap(ptr: any): any;
    __destroy_into_raw(): number | undefined;
    __wbg_ptr: number | undefined;
    free(): void;
    /**
     * @param {SigningNonce} arg0
     */
    set nonce(arg0: SigningNonce);
    /**
     * @returns {SigningNonce}
     */
    get nonce(): SigningNonce;
    /**
     * @param {SigningCommitment} arg0
     */
    set commitment(arg0: SigningCommitment);
    /**
     * @returns {SigningCommitment}
     */
    get commitment(): SigningCommitment;
}
export class SigningCommitment {
    static __wrap(ptr: any): any;
    /**
     * @param {Uint8Array} hiding
     * @param {Uint8Array} binding
     */
    constructor(hiding: Uint8Array, binding: Uint8Array);
    __destroy_into_raw(): number;
    __wbg_ptr: number;
    free(): void;
    /**
     * @param {Uint8Array} arg0
     */
    set hiding(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get hiding(): Uint8Array;
    /**
     * @param {Uint8Array} arg0
     */
    set binding(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get binding(): Uint8Array;
}
export class SigningNonce {
    static __wrap(ptr: any): any;
    /**
     * @param {Uint8Array} hiding
     * @param {Uint8Array} binding
     */
    constructor(hiding: Uint8Array, binding: Uint8Array);
    __destroy_into_raw(): number;
    __wbg_ptr: number;
    free(): void;
    /**
     * @param {Uint8Array} arg0
     */
    set hiding(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get hiding(): Uint8Array;
    /**
     * @param {Uint8Array} arg0
     */
    set binding(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get binding(): Uint8Array;
}
export class TransactionResult {
    static __wrap(ptr: any): any;
    __destroy_into_raw(): number | undefined;
    __wbg_ptr: number | undefined;
    free(): void;
    /**
     * @param {Uint8Array} arg0
     */
    set tx(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get tx(): Uint8Array;
    /**
     * @param {Uint8Array} arg0
     */
    set sighash(arg0: Uint8Array);
    /**
     * @returns {Uint8Array}
     */
    get sighash(): Uint8Array;
}
export default __wbg_init;
export function initSync(module: any): any;
declare function __wbg_init(module_or_path: any): Promise<any>;
