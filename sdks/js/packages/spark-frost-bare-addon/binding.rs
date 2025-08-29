use bare_rust::{
    ffi::{js_env_t, js_value_t},
    Env, Function, Object, String, Uint8Array, TypedArray, Value, BigInt, Array
};
use spark_frost::bridge::{create_dummy_tx};

use frost_secp256k1_tr::Identifier;
use hex;
use std::collections::HashMap;

macro_rules! log_binding {
    ($($arg:tt)*) => {
        println!("binding.rs: {}", format!($($arg)*));
    };
}

fn js_error(env: &Env, msg: &str) -> Value {
    String::new(env, msg).unwrap().into()
}

fn js_err(env: &Env, msg: &str) -> Value { js_error(env, msg) }

// Convert JS array of [key, value] pairs into a Rust HashMap using a mapper fn.
fn js_pairs_to_map<T, F>(env: &Env, arr: &Array, mut mapper: F) -> Result<HashMap<std::string::String, T>, Value>
where
    F: FnMut(&Env, Value) -> Result<T, Value>,
{
    let mut map = HashMap::new();
    for i in 0..arr.len() {
        let pair_val: Value = arr.get(i)?;
        let pair_arr: Array = pair_val.into();
        if pair_arr.len() != 2 {
            return Err(js_err(env, "pair length must be 2"));
        }
        let key_js: String = pair_arr.get(0)?;
        let key: std::string::String = key_js.into();
        let val_js: Value = pair_arr.get(1)?;
        let val_mapped = mapper(env, val_js)?;
        map.insert(key, val_mapped);
    }
    Ok(map)
}

// Helper to convert a JS Uint8Array property to Vec<u8>
fn get_uint8_vec(env: &Env, obj: &Object, name: &str) -> Result<Vec<u8>, Value> {
    let arr: Uint8Array = obj
        .get_named_property(name)
        .map_err(|_| js_err(env, &format!("missing field {name}")))?;
    Ok(arr.as_slice().to_vec())
}

// JsCommitment { hiding: Uint8Array, binding: Uint8Array }
fn js_commitment_to_proto(env: &Env, obj: &Object) -> Result<spark_frost::proto::common::SigningCommitment, Value> {
    let hiding = get_uint8_vec(env, obj, "hiding")?;
    let binding = get_uint8_vec(env, obj, "binding")?;
    Ok(spark_frost::proto::common::SigningCommitment { hiding, binding })
}

/// JsNonce { hiding: Uint8Array, binding: Uint8Array }
fn js_nonce_to_proto(env: &Env, obj: &Object) -> Result<spark_frost::proto::frost::SigningNonce, Value> {
    let hiding = get_uint8_vec(env, obj, "hiding")?;
    let binding = get_uint8_vec(env, obj, "binding")?;
    Ok(spark_frost::proto::frost::SigningNonce { hiding, binding })
}

#[unsafe(no_mangle)]
pub extern "C" fn bare_addon_exports(
    env: *mut js_env_t,
    _exports: *mut js_value_t,
) -> *mut js_value_t {
    let env = Env::from(env);

    let mut exports = Object::new(&env).unwrap();

    let function = Function::new(&env, |env, _| {
        Ok(String::new(env, "Hello from Rust")?.into())
    }).unwrap();

    exports
        .set_named_property("hello", function)
        .unwrap();

    // createDummyTx(address: string, amountSats: bigint | number) -> { tx: Uint8Array, txid: string }
    let create_dummy_tx_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let js_addr: String = info.arg(0).ok_or(js_err(env, "address argument missing or not a string"))?;

        let address: std::string::String = js_addr.into();

        let bigint: BigInt = info.arg(1).ok_or(js_err(env, "amountSats argument missing or not a bigint"))?;
        let amount = u64::from(bigint);

        match create_dummy_tx(&address, amount) {
            Ok(dummy) => {
                let mut obj = Object::new(env)?;
                let tx_arr = Uint8Array::new(env, dummy.tx.len())?;
                tx_arr.as_mut_slice().copy_from_slice(&dummy.tx);
                obj.set_named_property("tx", tx_arr)?;
                obj.set_named_property("txid", String::new(env, &dummy.txid)? )?;
                Ok(obj.into())
            }
            Err(e) => {
                Err(js_err(env, &format!("failed to create dummy tx: {}", e)))
            },
        }
    }).unwrap();

    exports
        .set_named_property("createDummyTx", create_dummy_tx_fn)
        .unwrap();

    // encryptEcies(msg: Uint8Array, publicKey: Uint8Array) -> Uint8Array
    let encrypt_ecies_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let msg_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "msg argument missing or not a Uint8Array"))?;
        let pk_arr: Uint8Array = info.arg(1).ok_or(js_err(env, "publicKey argument missing or not a Uint8Array"))?;

        let ciphertext = match spark_frost::bridge::encrypt_ecies(msg_arr.as_slice(), pk_arr.as_slice()) {
            Ok(c) => c,
            Err(e) => return Err(js_err(env, &format!("encrypt error: {}", e))),
        };

        let js_cipher = Uint8Array::new(env, ciphertext.len())?;
        js_cipher.as_mut_slice().copy_from_slice(&ciphertext);
        Ok(js_cipher.into())
    }).unwrap();

    exports.set_named_property("encryptEcies", encrypt_ecies_fn).unwrap();

    // decryptEcies(ciphertext: Uint8Array, secretKey: Uint8Array) -> Uint8Array
    let decrypt_ecies_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let ct_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "ciphertext argument missing or not a Uint8Array"))?;
        let sk_arr: Uint8Array = info.arg(1).ok_or(js_err(env, "secretKey argument missing or not a Uint8Array"))?;

        let plaintext = match spark_frost::bridge::decrypt_ecies(ct_arr.as_slice().to_vec(), sk_arr.as_slice().to_vec()) {
            Ok(p) => p,
            Err(e) => return Err(js_err(env, &format!("decrypt error: {}", e))),
        };

        let js_plaintext = Uint8Array::new(env, plaintext.len())?;
        js_plaintext.as_mut_slice().copy_from_slice(&plaintext);
        Ok(js_plaintext.into())
    }).unwrap();

    exports.set_named_property("decryptEcies", decrypt_ecies_fn).unwrap();

    // signFrost(msg, keyPackage, nonce, selfCommitment, statechainCommitments?, adaptorPubKey?)
    let sign_frost_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        // msg
        let msg_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "msg argument missing"))?;

        // keyPackage
        let kp_obj: Object = info.arg(1).ok_or(js_err(env, "keyPackage argument missing"))?;
        let secret_key = get_uint8_vec(env, &kp_obj, "secretKey")?;
        let public_key = get_uint8_vec(env, &kp_obj, "publicKey")?;
        let verifying_key = get_uint8_vec(env, &kp_obj, "verifyingKey")?;

        // Build proto KeyPackage
        let identifier = Identifier::derive(b"user").map_err(|e| js_err(env, &e.to_string()))?;
        let identifier_string = hex::encode(identifier.to_scalar().to_bytes());
        let kp_proto = spark_frost::proto::frost::KeyPackage {
            identifier: identifier_string.clone(),
            secret_share: secret_key.clone(),
            public_shares: HashMap::from([(identifier_string.clone(), public_key.clone())]),
            public_key: verifying_key.clone(),
            min_signers: 1,
        };

        // nonce
        let nonce_obj: Object = info.arg(2).ok_or(js_err(env, "nonce argument missing"))?;
        let nonce_proto = js_nonce_to_proto(env, &nonce_obj)?;

        // self commitment: JsCommitment
        let self_commit_obj: Object = info.arg(3).ok_or(js_err(env, "selfCommitment argument missing"))?;
        let self_commit_proto = js_commitment_to_proto(env, &self_commit_obj)?;

        // commitments array arg4: [[key, JsCommitment], ...]
        let commit_arr: Array = info.arg(4).ok_or(js_err(env, "commitments argument missing"))?;
        let commitments_proto = js_pairs_to_map(env, &commit_arr, |env, val| {
            let obj: Object = val.into();
            js_commitment_to_proto(env, &obj)
        })?;

        // adaptor public key (optional): Uint8Array
        let adaptor_pk: Option<Vec<u8>> = info.arg(5).map(|a: Uint8Array| a.as_slice().to_vec());

        match spark_frost::bridge::sign_frost(
            msg_arr.as_slice().to_vec(),
            kp_proto,
            nonce_proto,
            self_commit_proto,
            commitments_proto,
            adaptor_pk,
        ) {
            Ok(sig) => {
                let js_sig = Uint8Array::new(env, sig.len())?;
                js_sig.as_mut_slice().copy_from_slice(&sig);
                Ok(js_sig.into())
            }
            Err(e) => Err(js_err(env, &e)),
        }
    }).unwrap();

    exports.set_named_property("signFrost", sign_frost_fn).unwrap();

    // aggregateFrost(msg, statechainCommitments, selfCommitment, statechainSignatures, selfSignature, statechainPublicKeys, selfPublicKey, verifyingKey, adaptorPublicKey)
    let aggregate_frost_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        // msg arg0: Uint8Array
        let msg_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "msg argument missing"))?;

        // statechainCommitments arg1: [[id, JsCommitment], ...]
        let comm_arr: Array = info.arg(1).ok_or(js_err(env, "statechainCommitments arg missing"))?;
        let commitments_proto = js_pairs_to_map(env, &comm_arr, |env, val| {
            let obj: Object = val.into();
            js_commitment_to_proto(env, &obj)
        })?;

        // selfCommitment arg2: JsCommitment
        let self_commit_obj: Object = info.arg(2).ok_or(js_err(env, "selfCommitment arg missing"))?;
        let self_commit_proto = js_commitment_to_proto(env, &self_commit_obj)?;

        // statechainSignatures arg3: [[id, Uint8Array], ...]
        let sig_arr: Array = info.arg(3).ok_or(js_err(env, "statechainSignatures arg missing"))?;
        let statechain_signatures = js_pairs_to_map(env, &sig_arr, |_env, val| {
            let ua: Uint8Array = val.into();
            Ok(ua.as_slice().to_vec())
        })?;

        // selfSignature arg4: Uint8Array
        let self_signature: Uint8Array = info.arg(4).ok_or(js_err(env, "selfSignature arg missing"))?;
        let self_signature_bytes = self_signature.as_slice().to_vec();

        // statechainPublicKeys arg5: [[id, Uint8Array], ...]
        let pk_arr: Array = info.arg(5).ok_or(js_err(env, "statechainPublicKeys arg missing"))?;
        let statechain_public_keys = js_pairs_to_map(env, &pk_arr, |_env, val| {
            let ua: Uint8Array = val.into();
            Ok(ua.as_slice().to_vec())
        })?;

        // selfPublicKey arg6: Uint8Array
        let self_public_key: Uint8Array = info.arg(6).ok_or(js_err(env, "selfPublicKey arg missing"))?;
        let public_key = self_public_key.as_slice().to_vec();

        // verifyingKey arg7: Uint8Array
        let verifying_key_arr: Uint8Array = info.arg(7).ok_or(js_err(env, "verifyingKey arg missing"))?;
        let verifying_key = verifying_key_arr.as_slice().to_vec();

        // adaptorPublicKey arg8: Uint8Array (optional)
        let adaptor_pk: Option<Vec<u8>> = info.arg(8).map(|a: Uint8Array| a.as_slice().to_vec());

        match spark_frost::bridge::aggregate_frost(
            msg_arr.as_slice().to_vec(),
            commitments_proto,
            self_commit_proto,
            statechain_signatures,
            self_signature_bytes,
            statechain_public_keys,
            public_key,
            verifying_key,
            adaptor_pk,
        ) {
            Ok(sig) => {
                let js_sig = Uint8Array::new(env, sig.len())?;
                js_sig.as_mut_slice().copy_from_slice(&sig);
                Ok(js_sig.into())
            }
            Err(e) => Err(js_err(env, &e)),
        }
    }).unwrap();

    exports.set_named_property("aggregateFrost", aggregate_frost_fn).unwrap();

    exports.into()
}
