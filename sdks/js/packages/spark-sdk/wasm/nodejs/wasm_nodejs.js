
let imports = {};
imports['__wbindgen_placeholder__'] = module.exports;

let cachedUint8ArrayMemory0 = null;

function getUint8ArrayMemory0() {
    if (cachedUint8ArrayMemory0 === null || cachedUint8ArrayMemory0.byteLength === 0) {
        cachedUint8ArrayMemory0 = new Uint8Array(wasm.memory.buffer);
    }
    return cachedUint8ArrayMemory0;
}

let cachedTextDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });

cachedTextDecoder.decode();

function decodeText(ptr, len) {
    return cachedTextDecoder.decode(getUint8ArrayMemory0().subarray(ptr, ptr + len));
}

function getStringFromWasm0(ptr, len) {
    ptr = ptr >>> 0;
    return decodeText(ptr, len);
}

let WASM_VECTOR_LEN = 0;

const cachedTextEncoder = new TextEncoder();

if (!('encodeInto' in cachedTextEncoder)) {
    cachedTextEncoder.encodeInto = function (arg, view) {
        const buf = cachedTextEncoder.encode(arg);
        view.set(buf);
        return {
            read: arg.length,
            written: buf.length
        };
    }
}

function passStringToWasm0(arg, malloc, realloc) {

    if (realloc === undefined) {
        const buf = cachedTextEncoder.encode(arg);
        const ptr = malloc(buf.length, 1) >>> 0;
        getUint8ArrayMemory0().subarray(ptr, ptr + buf.length).set(buf);
        WASM_VECTOR_LEN = buf.length;
        return ptr;
    }

    let len = arg.length;
    let ptr = malloc(len, 1) >>> 0;

    const mem = getUint8ArrayMemory0();

    let offset = 0;

    for (; offset < len; offset++) {
        const code = arg.charCodeAt(offset);
        if (code > 0x7F) break;
        mem[ptr + offset] = code;
    }

    if (offset !== len) {
        if (offset !== 0) {
            arg = arg.slice(offset);
        }
        ptr = realloc(ptr, len, len = offset + arg.length * 3, 1) >>> 0;
        const view = getUint8ArrayMemory0().subarray(ptr + offset, ptr + len);
        const ret = cachedTextEncoder.encodeInto(arg, view);

        offset += ret.written;
        ptr = realloc(ptr, len, offset, 1) >>> 0;
    }

    WASM_VECTOR_LEN = offset;
    return ptr;
}

let cachedDataViewMemory0 = null;

function getDataViewMemory0() {
    if (cachedDataViewMemory0 === null || cachedDataViewMemory0.buffer.detached === true || (cachedDataViewMemory0.buffer.detached === undefined && cachedDataViewMemory0.buffer !== wasm.memory.buffer)) {
        cachedDataViewMemory0 = new DataView(wasm.memory.buffer);
    }
    return cachedDataViewMemory0;
}

function isLikeNone(x) {
    return x === undefined || x === null;
}

function debugString(val) {
    // primitive types
    const type = typeof val;
    if (type == 'number' || type == 'boolean' || val == null) {
        return  `${val}`;
    }
    if (type == 'string') {
        return `"${val}"`;
    }
    if (type == 'symbol') {
        const description = val.description;
        if (description == null) {
            return 'Symbol';
        } else {
            return `Symbol(${description})`;
        }
    }
    if (type == 'function') {
        const name = val.name;
        if (typeof name == 'string' && name.length > 0) {
            return `Function(${name})`;
        } else {
            return 'Function';
        }
    }
    // objects
    if (Array.isArray(val)) {
        const length = val.length;
        let debug = '[';
        if (length > 0) {
            debug += debugString(val[0]);
        }
        for(let i = 1; i < length; i++) {
            debug += ', ' + debugString(val[i]);
        }
        debug += ']';
        return debug;
    }
    // Test for built-in
    const builtInMatches = /\[object ([^\]]+)\]/.exec(toString.call(val));
    let className;
    if (builtInMatches && builtInMatches.length > 1) {
        className = builtInMatches[1];
    } else {
        // Failed to match the standard '[object ClassName]'
        return toString.call(val);
    }
    if (className == 'Object') {
        // we're a user defined class or Object
        // JSON.stringify avoids problems with cycles, and is generally much
        // easier than looping through ownProperties of `val`.
        try {
            return 'Object(' + JSON.stringify(val) + ')';
        } catch (_) {
            return 'Object';
        }
    }
    // errors
    if (val instanceof Error) {
        return `${val.name}: ${val.message}\n${val.stack}`;
    }
    // TODO we could test for more things here, like `Set`s and `Map`s.
    return className;
}

function addToExternrefTable0(obj) {
    const idx = wasm.__externref_table_alloc();
    wasm.__wbindgen_externrefs.set(idx, obj);
    return idx;
}

function handleError(f, args) {
    try {
        return f.apply(this, args);
    } catch (e) {
        const idx = addToExternrefTable0(e);
        wasm.__wbindgen_exn_store(idx);
    }
}

function getArrayU8FromWasm0(ptr, len) {
    ptr = ptr >>> 0;
    return getUint8ArrayMemory0().subarray(ptr / 1, ptr / 1 + len);
}

function passArray8ToWasm0(arg, malloc) {
    const ptr = malloc(arg.length * 1, 1) >>> 0;
    getUint8ArrayMemory0().set(arg, ptr / 1);
    WASM_VECTOR_LEN = arg.length;
    return ptr;
}

function _assertClass(instance, klass) {
    if (!(instance instanceof klass)) {
        throw new Error(`expected instance of ${klass.name}`);
    }
}

function takeFromExternrefTable0(idx) {
    const value = wasm.__wbindgen_externrefs.get(idx);
    wasm.__externref_table_dealloc(idx);
    return value;
}
/**
 * @param {KeyPackage} key_package
 * @returns {NonceResult}
 */
exports.frost_nonce = function(key_package) {
    _assertClass(key_package, KeyPackage);
    var ptr0 = key_package.__destroy_into_raw();
    const ret = wasm.frost_nonce(ptr0);
    if (ret[2]) {
        throw takeFromExternrefTable0(ret[1]);
    }
    return NonceResult.__wrap(ret[0]);
};

/**
 * @param {Uint8Array} msg
 * @param {KeyPackage} key_package
 * @param {SigningNonce} nonce
 * @param {SigningCommitment} self_commitment
 * @param {any} statechain_commitments
 * @param {Uint8Array | null} [adaptor_public_key]
 * @returns {Uint8Array}
 */
exports.wasm_sign_frost = function(msg, key_package, nonce, self_commitment, statechain_commitments, adaptor_public_key) {
    const ptr0 = passArray8ToWasm0(msg, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    _assertClass(key_package, KeyPackage);
    var ptr1 = key_package.__destroy_into_raw();
    _assertClass(nonce, SigningNonce);
    var ptr2 = nonce.__destroy_into_raw();
    _assertClass(self_commitment, SigningCommitment);
    var ptr3 = self_commitment.__destroy_into_raw();
    var ptr4 = isLikeNone(adaptor_public_key) ? 0 : passArray8ToWasm0(adaptor_public_key, wasm.__wbindgen_malloc);
    var len4 = WASM_VECTOR_LEN;
    const ret = wasm.wasm_sign_frost(ptr0, len0, ptr1, ptr2, ptr3, statechain_commitments, ptr4, len4);
    if (ret[3]) {
        throw takeFromExternrefTable0(ret[2]);
    }
    var v6 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
    wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
    return v6;
};

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
exports.wasm_aggregate_frost = function(msg, statechain_commitments, self_commitment, statechain_signatures, self_signature, statechain_public_keys, self_public_key, verifying_key, adaptor_public_key) {
    const ptr0 = passArray8ToWasm0(msg, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    _assertClass(self_commitment, SigningCommitment);
    var ptr1 = self_commitment.__destroy_into_raw();
    const ptr2 = passArray8ToWasm0(self_signature, wasm.__wbindgen_malloc);
    const len2 = WASM_VECTOR_LEN;
    const ptr3 = passArray8ToWasm0(self_public_key, wasm.__wbindgen_malloc);
    const len3 = WASM_VECTOR_LEN;
    const ptr4 = passArray8ToWasm0(verifying_key, wasm.__wbindgen_malloc);
    const len4 = WASM_VECTOR_LEN;
    var ptr5 = isLikeNone(adaptor_public_key) ? 0 : passArray8ToWasm0(adaptor_public_key, wasm.__wbindgen_malloc);
    var len5 = WASM_VECTOR_LEN;
    const ret = wasm.wasm_aggregate_frost(ptr0, len0, statechain_commitments, ptr1, statechain_signatures, ptr2, len2, statechain_public_keys, ptr3, len3, ptr4, len4, ptr5, len5);
    if (ret[3]) {
        throw takeFromExternrefTable0(ret[2]);
    }
    var v7 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
    wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
    return v7;
};

function getArrayJsValueFromWasm0(ptr, len) {
    ptr = ptr >>> 0;
    const mem = getDataViewMemory0();
    const result = [];
    for (let i = ptr; i < ptr + 4 * len; i += 4) {
        result.push(wasm.__wbindgen_externrefs.get(mem.getUint32(i, true)));
    }
    wasm.__externref_drop_slice(ptr, len);
    return result;
}

function passArrayJsValueToWasm0(array, malloc) {
    const ptr = malloc(array.length * 4, 4) >>> 0;
    for (let i = 0; i < array.length; i++) {
        const add = addToExternrefTable0(array[i]);
        getDataViewMemory0().setUint32(ptr + 4 * i, add, true);
    }
    WASM_VECTOR_LEN = array.length;
    return ptr;
}
/**
 * @param {Uint8Array} tx
 * @param {number} vout
 * @param {string} address
 * @param {number} locktime
 * @returns {TransactionResult}
 */
exports.construct_node_tx = function(tx, vout, address, locktime) {
    const ptr0 = passArray8ToWasm0(tx, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passStringToWasm0(address, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len1 = WASM_VECTOR_LEN;
    const ret = wasm.construct_node_tx(ptr0, len0, vout, ptr1, len1, locktime);
    if (ret[2]) {
        throw takeFromExternrefTable0(ret[1]);
    }
    return TransactionResult.__wrap(ret[0]);
};

/**
 * @param {Uint8Array} tx
 * @param {number} vout
 * @param {Uint8Array} pubkey
 * @param {string} network
 * @param {number} sequence
 * @returns {TransactionResult}
 */
exports.construct_refund_tx = function(tx, vout, pubkey, network, sequence) {
    const ptr0 = passArray8ToWasm0(tx, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArray8ToWasm0(pubkey, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passStringToWasm0(network, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len2 = WASM_VECTOR_LEN;
    const ret = wasm.construct_refund_tx(ptr0, len0, vout, ptr1, len1, ptr2, len2, sequence);
    if (ret[2]) {
        throw takeFromExternrefTable0(ret[1]);
    }
    return TransactionResult.__wrap(ret[0]);
};

/**
 * @param {Uint8Array} tx
 * @param {number} vout
 * @param {string[]} addresses
 * @param {number} locktime
 * @returns {TransactionResult}
 */
exports.construct_split_tx = function(tx, vout, addresses, locktime) {
    const ptr0 = passArray8ToWasm0(tx, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArrayJsValueToWasm0(addresses, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ret = wasm.construct_split_tx(ptr0, len0, vout, ptr1, len1, locktime);
    if (ret[2]) {
        throw takeFromExternrefTable0(ret[1]);
    }
    return TransactionResult.__wrap(ret[0]);
};

/**
 * @param {Uint8Array} tx
 * @param {number} vout
 * @param {Uint8Array} pubkey
 * @param {string} network
 * @param {number} sequence
 * @returns {TransactionResult}
 */
exports.construct_direct_refund_tx = function(tx, vout, pubkey, network, sequence) {
    const ptr0 = passArray8ToWasm0(tx, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArray8ToWasm0(pubkey, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passStringToWasm0(network, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len2 = WASM_VECTOR_LEN;
    const ret = wasm.construct_direct_refund_tx(ptr0, len0, vout, ptr1, len1, ptr2, len2, sequence);
    if (ret[2]) {
        throw takeFromExternrefTable0(ret[1]);
    }
    return TransactionResult.__wrap(ret[0]);
};

/**
 * @param {string} address
 * @param {bigint} amount_sats
 * @returns {DummyTx}
 */
exports.create_dummy_tx = function(address, amount_sats) {
    const ptr0 = passStringToWasm0(address, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len0 = WASM_VECTOR_LEN;
    const ret = wasm.create_dummy_tx(ptr0, len0, amount_sats);
    if (ret[2]) {
        throw takeFromExternrefTable0(ret[1]);
    }
    return DummyTx.__wrap(ret[0]);
};

/**
 * @param {Uint8Array} msg
 * @param {Uint8Array} public_key_bytes
 * @returns {Uint8Array}
 */
exports.encrypt_ecies = function(msg, public_key_bytes) {
    const ptr0 = passArray8ToWasm0(msg, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArray8ToWasm0(public_key_bytes, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ret = wasm.encrypt_ecies(ptr0, len0, ptr1, len1);
    if (ret[3]) {
        throw takeFromExternrefTable0(ret[2]);
    }
    var v3 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
    wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
    return v3;
};

/**
 * @param {Uint8Array} encrypted_msg
 * @param {Uint8Array} private_key_bytes
 * @returns {Uint8Array}
 */
exports.decrypt_ecies = function(encrypted_msg, private_key_bytes) {
    const ptr0 = passArray8ToWasm0(encrypted_msg, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArray8ToWasm0(private_key_bytes, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ret = wasm.decrypt_ecies(ptr0, len0, ptr1, len1);
    if (ret[3]) {
        throw takeFromExternrefTable0(ret[2]);
    }
    var v3 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
    wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
    return v3;
};

/**
 * @param {Uint8Array} verifying_pubkey
 * @returns {Uint8Array}
 */
exports.get_taproot_pubkey = function(verifying_pubkey) {
    const ptr0 = passArray8ToWasm0(verifying_pubkey, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ret = wasm.get_taproot_pubkey(ptr0, len0);
    if (ret[3]) {
        throw takeFromExternrefTable0(ret[2]);
    }
    var v2 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
    wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
    return v2;
};

/**
 * @param {Uint8Array} private_key_bytes
 * @param {boolean} compressed
 * @returns {Uint8Array}
 */
exports.get_public_key_bytes = function(private_key_bytes, compressed) {
    const ptr0 = passArray8ToWasm0(private_key_bytes, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ret = wasm.get_public_key_bytes(ptr0, len0, compressed);
    if (ret[3]) {
        throw takeFromExternrefTable0(ret[2]);
    }
    var v2 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
    wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
    return v2;
};

/**
 * @param {Uint8Array} signature
 * @param {Uint8Array} message
 * @param {Uint8Array} public_key
 * @returns {boolean}
 */
exports.verify_signature_bytes = function(signature, message, public_key) {
    const ptr0 = passArray8ToWasm0(signature, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArray8ToWasm0(message, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passArray8ToWasm0(public_key, wasm.__wbindgen_malloc);
    const len2 = WASM_VECTOR_LEN;
    const ret = wasm.verify_signature_bytes(ptr0, len0, ptr1, len1, ptr2, len2);
    if (ret[2]) {
        throw takeFromExternrefTable0(ret[1]);
    }
    return ret[0] !== 0;
};

/**
 * @returns {Uint8Array}
 */
exports.random_secret_key_bytes = function() {
    const ret = wasm.random_secret_key_bytes();
    if (ret[3]) {
        throw takeFromExternrefTable0(ret[2]);
    }
    var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
    wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
    return v1;
};

const DummyTxFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_dummytx_free(ptr >>> 0, 1));

class DummyTx {

    static __wrap(ptr) {
        ptr = ptr >>> 0;
        const obj = Object.create(DummyTx.prototype);
        obj.__wbg_ptr = ptr;
        DummyTxFinalization.register(obj, obj.__wbg_ptr, obj);
        return obj;
    }

    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        DummyTxFinalization.unregister(this);
        return ptr;
    }

    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_dummytx_free(ptr, 0);
    }
    /**
     * @returns {Uint8Array}
     */
    get tx() {
        const ret = wasm.__wbg_get_dummytx_tx(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set tx(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_tx(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @returns {string}
     */
    get txid() {
        let deferred1_0;
        let deferred1_1;
        try {
            const ret = wasm.__wbg_get_dummytx_txid(this.__wbg_ptr);
            deferred1_0 = ret[0];
            deferred1_1 = ret[1];
            return getStringFromWasm0(ret[0], ret[1]);
        } finally {
            wasm.__wbindgen_free(deferred1_0, deferred1_1, 1);
        }
    }
    /**
     * @param {string} arg0
     */
    set txid(arg0) {
        const ptr0 = passStringToWasm0(arg0, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_txid(this.__wbg_ptr, ptr0, len0);
    }
}
if (Symbol.dispose) DummyTx.prototype[Symbol.dispose] = DummyTx.prototype.free;

exports.DummyTx = DummyTx;

const KeyPackageFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_keypackage_free(ptr >>> 0, 1));

class KeyPackage {

    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        KeyPackageFinalization.unregister(this);
        return ptr;
    }

    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_keypackage_free(ptr, 0);
    }
    /**
     * @returns {Uint8Array}
     */
    get secret_key() {
        const ret = wasm.__wbg_get_keypackage_secret_key(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set secret_key(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_tx(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @returns {Uint8Array}
     */
    get public_key() {
        const ret = wasm.__wbg_get_keypackage_public_key(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set public_key(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_txid(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @returns {Uint8Array}
     */
    get verifying_key() {
        const ret = wasm.__wbg_get_keypackage_verifying_key(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set verifying_key(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_keypackage_verifying_key(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @param {Uint8Array} secret_key
     * @param {Uint8Array} public_key
     * @param {Uint8Array} verifying_key
     */
    constructor(secret_key, public_key, verifying_key) {
        const ptr0 = passArray8ToWasm0(secret_key, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passArray8ToWasm0(public_key, wasm.__wbindgen_malloc);
        const len1 = WASM_VECTOR_LEN;
        const ptr2 = passArray8ToWasm0(verifying_key, wasm.__wbindgen_malloc);
        const len2 = WASM_VECTOR_LEN;
        const ret = wasm.keypackage_new(ptr0, len0, ptr1, len1, ptr2, len2);
        this.__wbg_ptr = ret >>> 0;
        KeyPackageFinalization.register(this, this.__wbg_ptr, this);
        return this;
    }
}
if (Symbol.dispose) KeyPackage.prototype[Symbol.dispose] = KeyPackage.prototype.free;

exports.KeyPackage = KeyPackage;

const NonceResultFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_nonceresult_free(ptr >>> 0, 1));

class NonceResult {

    static __wrap(ptr) {
        ptr = ptr >>> 0;
        const obj = Object.create(NonceResult.prototype);
        obj.__wbg_ptr = ptr;
        NonceResultFinalization.register(obj, obj.__wbg_ptr, obj);
        return obj;
    }

    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        NonceResultFinalization.unregister(this);
        return ptr;
    }

    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_nonceresult_free(ptr, 0);
    }
    /**
     * @returns {SigningNonce}
     */
    get nonce() {
        const ret = wasm.__wbg_get_nonceresult_nonce(this.__wbg_ptr);
        return SigningNonce.__wrap(ret);
    }
    /**
     * @param {SigningNonce} arg0
     */
    set nonce(arg0) {
        _assertClass(arg0, SigningNonce);
        var ptr0 = arg0.__destroy_into_raw();
        wasm.__wbg_set_nonceresult_nonce(this.__wbg_ptr, ptr0);
    }
    /**
     * @returns {SigningCommitment}
     */
    get commitment() {
        const ret = wasm.__wbg_get_nonceresult_commitment(this.__wbg_ptr);
        return SigningCommitment.__wrap(ret);
    }
    /**
     * @param {SigningCommitment} arg0
     */
    set commitment(arg0) {
        _assertClass(arg0, SigningCommitment);
        var ptr0 = arg0.__destroy_into_raw();
        wasm.__wbg_set_nonceresult_commitment(this.__wbg_ptr, ptr0);
    }
}
if (Symbol.dispose) NonceResult.prototype[Symbol.dispose] = NonceResult.prototype.free;

exports.NonceResult = NonceResult;

const SigningCommitmentFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_signingcommitment_free(ptr >>> 0, 1));

class SigningCommitment {

    static __wrap(ptr) {
        ptr = ptr >>> 0;
        const obj = Object.create(SigningCommitment.prototype);
        obj.__wbg_ptr = ptr;
        SigningCommitmentFinalization.register(obj, obj.__wbg_ptr, obj);
        return obj;
    }

    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        SigningCommitmentFinalization.unregister(this);
        return ptr;
    }

    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_signingcommitment_free(ptr, 0);
    }
    /**
     * @returns {Uint8Array}
     */
    get hiding() {
        const ret = wasm.__wbg_get_signingcommitment_hiding(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set hiding(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_tx(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @returns {Uint8Array}
     */
    get binding() {
        const ret = wasm.__wbg_get_signingcommitment_binding(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set binding(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_txid(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @param {Uint8Array} hiding
     * @param {Uint8Array} binding
     */
    constructor(hiding, binding) {
        const ptr0 = passArray8ToWasm0(hiding, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passArray8ToWasm0(binding, wasm.__wbindgen_malloc);
        const len1 = WASM_VECTOR_LEN;
        const ret = wasm.signingcommitment_new(ptr0, len0, ptr1, len1);
        this.__wbg_ptr = ret >>> 0;
        SigningCommitmentFinalization.register(this, this.__wbg_ptr, this);
        return this;
    }
}
if (Symbol.dispose) SigningCommitment.prototype[Symbol.dispose] = SigningCommitment.prototype.free;

exports.SigningCommitment = SigningCommitment;

const SigningNonceFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_signingnonce_free(ptr >>> 0, 1));

class SigningNonce {

    static __wrap(ptr) {
        ptr = ptr >>> 0;
        const obj = Object.create(SigningNonce.prototype);
        obj.__wbg_ptr = ptr;
        SigningNonceFinalization.register(obj, obj.__wbg_ptr, obj);
        return obj;
    }

    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        SigningNonceFinalization.unregister(this);
        return ptr;
    }

    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_signingnonce_free(ptr, 0);
    }
    /**
     * @returns {Uint8Array}
     */
    get hiding() {
        const ret = wasm.__wbg_get_signingnonce_hiding(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set hiding(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_tx(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @returns {Uint8Array}
     */
    get binding() {
        const ret = wasm.__wbg_get_signingnonce_binding(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set binding(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_txid(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @param {Uint8Array} hiding
     * @param {Uint8Array} binding
     */
    constructor(hiding, binding) {
        const ptr0 = passArray8ToWasm0(hiding, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passArray8ToWasm0(binding, wasm.__wbindgen_malloc);
        const len1 = WASM_VECTOR_LEN;
        const ret = wasm.signingcommitment_new(ptr0, len0, ptr1, len1);
        this.__wbg_ptr = ret >>> 0;
        SigningNonceFinalization.register(this, this.__wbg_ptr, this);
        return this;
    }
}
if (Symbol.dispose) SigningNonce.prototype[Symbol.dispose] = SigningNonce.prototype.free;

exports.SigningNonce = SigningNonce;

const TransactionResultFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_transactionresult_free(ptr >>> 0, 1));

class TransactionResult {

    static __wrap(ptr) {
        ptr = ptr >>> 0;
        const obj = Object.create(TransactionResult.prototype);
        obj.__wbg_ptr = ptr;
        TransactionResultFinalization.register(obj, obj.__wbg_ptr, obj);
        return obj;
    }

    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        TransactionResultFinalization.unregister(this);
        return ptr;
    }

    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_transactionresult_free(ptr, 0);
    }
    /**
     * @returns {Uint8Array}
     */
    get tx() {
        const ret = wasm.__wbg_get_transactionresult_tx(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set tx(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_tx(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @returns {Uint8Array}
     */
    get sighash() {
        const ret = wasm.__wbg_get_transactionresult_sighash(this.__wbg_ptr);
        var v1 = getArrayU8FromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 1, 1);
        return v1;
    }
    /**
     * @param {Uint8Array} arg0
     */
    set sighash(arg0) {
        const ptr0 = passArray8ToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_dummytx_txid(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * @returns {TxIn[]}
     */
    get inputs() {
        const ret = wasm.__wbg_get_transactionresult_inputs(this.__wbg_ptr);
        var v1 = getArrayJsValueFromWasm0(ret[0], ret[1]).slice();
        wasm.__wbindgen_free(ret[0], ret[1] * 4, 4);
        return v1;
    }
    /**
     * @param {TxIn[]} arg0
     */
    set inputs(arg0) {
        const ptr0 = passArrayJsValueToWasm0(arg0, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.__wbg_set_transactionresult_inputs(this.__wbg_ptr, ptr0, len0);
    }
}
if (Symbol.dispose) TransactionResult.prototype[Symbol.dispose] = TransactionResult.prototype.free;

exports.TransactionResult = TransactionResult;

const TxInFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_txin_free(ptr >>> 0, 1));
/**
 * A stand-in for TxIn.
 */
class TxIn {

    static __wrap(ptr) {
        ptr = ptr >>> 0;
        const obj = Object.create(TxIn.prototype);
        obj.__wbg_ptr = ptr;
        TxInFinalization.register(obj, obj.__wbg_ptr, obj);
        return obj;
    }

    static __unwrap(jsValue) {
        if (!(jsValue instanceof TxIn)) {
            return 0;
        }
        return jsValue.__destroy_into_raw();
    }

    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        TxInFinalization.unregister(this);
        return ptr;
    }

    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_txin_free(ptr, 0);
    }
    /**
     * @returns {number}
     */
    get sequence() {
        const ret = wasm.__wbg_get_txin_sequence(this.__wbg_ptr);
        return ret >>> 0;
    }
    /**
     * @param {number} arg0
     */
    set sequence(arg0) {
        wasm.__wbg_set_txin_sequence(this.__wbg_ptr, arg0);
    }
}
if (Symbol.dispose) TxIn.prototype[Symbol.dispose] = TxIn.prototype.free;

exports.TxIn = TxIn;

exports.__wbg_Error_e83987f665cf5504 = function(arg0, arg1) {
    const ret = Error(getStringFromWasm0(arg0, arg1));
    return ret;
};

exports.__wbg_Number_bb48ca12f395cd08 = function(arg0) {
    const ret = Number(arg0);
    return ret;
};

exports.__wbg_String_8f0eb39a4a4c2f66 = function(arg0, arg1) {
    const ret = String(arg1);
    const ptr1 = passStringToWasm0(ret, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len1 = WASM_VECTOR_LEN;
    getDataViewMemory0().setInt32(arg0 + 4 * 1, len1, true);
    getDataViewMemory0().setInt32(arg0 + 4 * 0, ptr1, true);
};

exports.__wbg___wbindgen_boolean_get_6d5a1ee65bab5f68 = function(arg0) {
    const v = arg0;
    const ret = typeof(v) === 'boolean' ? v : undefined;
    return isLikeNone(ret) ? 0xFFFFFF : ret ? 1 : 0;
};

exports.__wbg___wbindgen_debug_string_df47ffb5e35e6763 = function(arg0, arg1) {
    const ret = debugString(arg1);
    const ptr1 = passStringToWasm0(ret, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len1 = WASM_VECTOR_LEN;
    getDataViewMemory0().setInt32(arg0 + 4 * 1, len1, true);
    getDataViewMemory0().setInt32(arg0 + 4 * 0, ptr1, true);
};

exports.__wbg___wbindgen_in_bb933bd9e1b3bc0f = function(arg0, arg1) {
    const ret = arg0 in arg1;
    return ret;
};

exports.__wbg___wbindgen_is_function_ee8a6c5833c90377 = function(arg0) {
    const ret = typeof(arg0) === 'function';
    return ret;
};

exports.__wbg___wbindgen_is_object_c818261d21f283a4 = function(arg0) {
    const val = arg0;
    const ret = typeof(val) === 'object' && val !== null;
    return ret;
};

exports.__wbg___wbindgen_is_string_fbb76cb2940daafd = function(arg0) {
    const ret = typeof(arg0) === 'string';
    return ret;
};

exports.__wbg___wbindgen_is_undefined_2d472862bd29a478 = function(arg0) {
    const ret = arg0 === undefined;
    return ret;
};

exports.__wbg___wbindgen_jsval_loose_eq_b664b38a2f582147 = function(arg0, arg1) {
    const ret = arg0 == arg1;
    return ret;
};

exports.__wbg___wbindgen_number_get_a20bf9b85341449d = function(arg0, arg1) {
    const obj = arg1;
    const ret = typeof(obj) === 'number' ? obj : undefined;
    getDataViewMemory0().setFloat64(arg0 + 8 * 1, isLikeNone(ret) ? 0 : ret, true);
    getDataViewMemory0().setInt32(arg0 + 4 * 0, !isLikeNone(ret), true);
};

exports.__wbg___wbindgen_string_get_e4f06c90489ad01b = function(arg0, arg1) {
    const obj = arg1;
    const ret = typeof(obj) === 'string' ? obj : undefined;
    var ptr1 = isLikeNone(ret) ? 0 : passStringToWasm0(ret, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    var len1 = WASM_VECTOR_LEN;
    getDataViewMemory0().setInt32(arg0 + 4 * 1, len1, true);
    getDataViewMemory0().setInt32(arg0 + 4 * 0, ptr1, true);
};

exports.__wbg___wbindgen_throw_b855445ff6a94295 = function(arg0, arg1) {
    throw new Error(getStringFromWasm0(arg0, arg1));
};

exports.__wbg_call_525440f72fbfc0ea = function() { return handleError(function (arg0, arg1, arg2) {
    const ret = arg0.call(arg1, arg2);
    return ret;
}, arguments) };

exports.__wbg_call_e762c39fa8ea36bf = function() { return handleError(function (arg0, arg1) {
    const ret = arg0.call(arg1);
    return ret;
}, arguments) };

exports.__wbg_crypto_574e78ad8b13b65f = function(arg0) {
    const ret = arg0.crypto;
    return ret;
};

exports.__wbg_done_2042aa2670fb1db1 = function(arg0) {
    const ret = arg0.done;
    return ret;
};

exports.__wbg_entries_e171b586f8f6bdbf = function(arg0) {
    const ret = Object.entries(arg0);
    return ret;
};

exports.__wbg_getRandomValues_b8f5dbd5f3995a9e = function() { return handleError(function (arg0, arg1) {
    arg0.getRandomValues(arg1);
}, arguments) };

exports.__wbg_get_7bed016f185add81 = function(arg0, arg1) {
    const ret = arg0[arg1 >>> 0];
    return ret;
};

exports.__wbg_get_efcb449f58ec27c2 = function() { return handleError(function (arg0, arg1) {
    const ret = Reflect.get(arg0, arg1);
    return ret;
}, arguments) };

exports.__wbg_get_with_ref_key_1dc361bd10053bfe = function(arg0, arg1) {
    const ret = arg0[arg1];
    return ret;
};

exports.__wbg_instanceof_ArrayBuffer_70beb1189ca63b38 = function(arg0) {
    let result;
    try {
        result = arg0 instanceof ArrayBuffer;
    } catch (_) {
        result = false;
    }
    const ret = result;
    return ret;
};

exports.__wbg_instanceof_Uint8Array_20c8e73002f7af98 = function(arg0) {
    let result;
    try {
        result = arg0 instanceof Uint8Array;
    } catch (_) {
        result = false;
    }
    const ret = result;
    return ret;
};

exports.__wbg_isArray_96e0af9891d0945d = function(arg0) {
    const ret = Array.isArray(arg0);
    return ret;
};

exports.__wbg_isSafeInteger_d216eda7911dde36 = function(arg0) {
    const ret = Number.isSafeInteger(arg0);
    return ret;
};

exports.__wbg_iterator_e5822695327a3c39 = function() {
    const ret = Symbol.iterator;
    return ret;
};

exports.__wbg_length_69bca3cb64fc8748 = function(arg0) {
    const ret = arg0.length;
    return ret;
};

exports.__wbg_length_cdd215e10d9dd507 = function(arg0) {
    const ret = arg0.length;
    return ret;
};

exports.__wbg_msCrypto_a61aeb35a24c1329 = function(arg0) {
    const ret = arg0.msCrypto;
    return ret;
};

exports.__wbg_new_5a79be3ab53b8aa5 = function(arg0) {
    const ret = new Uint8Array(arg0);
    return ret;
};

exports.__wbg_new_no_args_ee98eee5275000a4 = function(arg0, arg1) {
    const ret = new Function(getStringFromWasm0(arg0, arg1));
    return ret;
};

exports.__wbg_new_with_length_01aa0dc35aa13543 = function(arg0) {
    const ret = new Uint8Array(arg0 >>> 0);
    return ret;
};

exports.__wbg_next_020810e0ae8ebcb0 = function() { return handleError(function (arg0) {
    const ret = arg0.next();
    return ret;
}, arguments) };

exports.__wbg_next_2c826fe5dfec6b6a = function(arg0) {
    const ret = arg0.next;
    return ret;
};

exports.__wbg_node_905d3e251edff8a2 = function(arg0) {
    const ret = arg0.node;
    return ret;
};

exports.__wbg_process_dc0fbacc7c1c06f7 = function(arg0) {
    const ret = arg0.process;
    return ret;
};

exports.__wbg_prototypesetcall_2a6620b6922694b2 = function(arg0, arg1, arg2) {
    Uint8Array.prototype.set.call(getArrayU8FromWasm0(arg0, arg1), arg2);
};

exports.__wbg_randomFillSync_ac0988aba3254290 = function() { return handleError(function (arg0, arg1) {
    arg0.randomFillSync(arg1);
}, arguments) };

exports.__wbg_require_60cc747a6bc5215a = function() { return handleError(function () {
    const ret = module.require;
    return ret;
}, arguments) };

exports.__wbg_static_accessor_GLOBAL_89e1d9ac6a1b250e = function() {
    const ret = typeof global === 'undefined' ? null : global;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
};

exports.__wbg_static_accessor_GLOBAL_THIS_8b530f326a9e48ac = function() {
    const ret = typeof globalThis === 'undefined' ? null : globalThis;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
};

exports.__wbg_static_accessor_SELF_6fdf4b64710cc91b = function() {
    const ret = typeof self === 'undefined' ? null : self;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
};

exports.__wbg_static_accessor_WINDOW_b45bfc5a37f6cfa2 = function() {
    const ret = typeof window === 'undefined' ? null : window;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
};

exports.__wbg_subarray_480600f3d6a9f26c = function(arg0, arg1, arg2) {
    const ret = arg0.subarray(arg1 >>> 0, arg2 >>> 0);
    return ret;
};

exports.__wbg_txin_new = function(arg0) {
    const ret = TxIn.__wrap(arg0);
    return ret;
};

exports.__wbg_txin_unwrap = function(arg0) {
    const ret = TxIn.__unwrap(arg0);
    return ret;
};

exports.__wbg_value_692627309814bb8c = function(arg0) {
    const ret = arg0.value;
    return ret;
};

exports.__wbg_versions_c01dfd4722a88165 = function(arg0) {
    const ret = arg0.versions;
    return ret;
};

exports.__wbindgen_cast_2241b6af4c4b2941 = function(arg0, arg1) {
    // Cast intrinsic for `Ref(String) -> Externref`.
    const ret = getStringFromWasm0(arg0, arg1);
    return ret;
};

exports.__wbindgen_cast_cb9088102bce6b30 = function(arg0, arg1) {
    // Cast intrinsic for `Ref(Slice(U8)) -> NamedExternref("Uint8Array")`.
    const ret = getArrayU8FromWasm0(arg0, arg1);
    return ret;
};

exports.__wbindgen_init_externref_table = function() {
    const table = wasm.__wbindgen_externrefs;
    const offset = table.grow(4);
    table.set(0, undefined);
    table.set(offset + 0, undefined);
    table.set(offset + 1, null);
    table.set(offset + 2, true);
    table.set(offset + 3, false);
    ;
};

const wasmPath = `${__dirname}/wasm_nodejs_bg.wasm`;
const wasmBytes = require('fs').readFileSync(wasmPath);
const wasmModule = new WebAssembly.Module(wasmBytes);
const wasm = exports.__wasm = new WebAssembly.Instance(wasmModule, imports).exports;

wasm.__wbindgen_start();

