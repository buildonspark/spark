use bare_rust::{
    ffi::{js_env_t, js_value_t},
    Array, BigInt, Env, Function, Number, Object, String, TypedArray, Uint8Array, Undefined, Value,
};
use spark_frost::bridge::create_dummy_tx;
use spark_frost::transaction::{
    compute_multi_input_sighash, construct_node_tx_pair, construct_refund_tx_trio,
};

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

fn js_err(env: &Env, msg: &str) -> Value {
    js_error(env, msg)
}

// Convert JS array of [key, value] pairs into a Rust HashMap using a mapper fn.
fn js_pairs_to_map<T, F>(
    env: &Env,
    arr: &Array,
    mut mapper: F,
) -> Result<HashMap<std::string::String, T>, Value>
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
fn js_commitment_to_proto(
    env: &Env,
    obj: &Object,
) -> Result<spark_frost::proto::common::SigningCommitment, Value> {
    let hiding = get_uint8_vec(env, obj, "hiding")?;
    let binding = get_uint8_vec(env, obj, "binding")?;
    Ok(spark_frost::proto::common::SigningCommitment { hiding, binding })
}

/// JsNonce { hiding: Uint8Array, binding: Uint8Array }
fn js_nonce_to_proto(
    env: &Env,
    obj: &Object,
) -> Result<spark_frost::proto::frost::SigningNonce, Value> {
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
    })
    .unwrap();

    exports.set_named_property("hello", function).unwrap();

    // createDummyTx(address: string, amountSats: bigint | number) -> { tx: Uint8Array, txid: string }
    let create_dummy_tx_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let js_addr: String = info
            .arg(0)
            .ok_or(js_err(env, "address argument missing or not a string"))?;

        let address: std::string::String = js_addr.into();

        let bigint: BigInt = info
            .arg(1)
            .ok_or(js_err(env, "amountSats argument missing or not a bigint"))?;
        let amount = u64::from(bigint);

        match create_dummy_tx(&address, amount) {
            Ok(dummy) => {
                let mut obj = Object::new(env)?;
                let tx_arr = Uint8Array::new(env, dummy.tx.len())?;
                tx_arr.as_mut_slice().copy_from_slice(&dummy.tx);
                obj.set_named_property("tx", tx_arr)?;
                obj.set_named_property("txid", String::new(env, &dummy.txid)?)?;
                Ok(obj.into())
            }
            Err(e) => Err(js_err(env, &format!("failed to create dummy tx: {}", e))),
        }
    })
    .unwrap();

    exports
        .set_named_property("createDummyTx", create_dummy_tx_fn)
        .unwrap();

    // encryptEcies(msg: Uint8Array, publicKey: Uint8Array) -> Uint8Array
    let encrypt_ecies_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let msg_arr: Uint8Array = info
            .arg(0)
            .ok_or(js_err(env, "msg argument missing or not a Uint8Array"))?;
        let pk_arr: Uint8Array = info.arg(1).ok_or(js_err(
            env,
            "publicKey argument missing or not a Uint8Array",
        ))?;

        let ciphertext =
            match spark_frost::bridge::encrypt_ecies(msg_arr.as_slice(), pk_arr.as_slice()) {
                Ok(c) => c,
                Err(e) => return Err(js_err(env, &format!("encrypt error: {}", e))),
            };

        let js_cipher = Uint8Array::new(env, ciphertext.len())?;
        js_cipher.as_mut_slice().copy_from_slice(&ciphertext);
        Ok(js_cipher.into())
    })
    .unwrap();

    exports
        .set_named_property("encryptEcies", encrypt_ecies_fn)
        .unwrap();

    // decryptEcies(ciphertext: Uint8Array, secretKey: Uint8Array) -> Uint8Array
    let decrypt_ecies_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let ct_arr: Uint8Array = info.arg(0).ok_or(js_err(
            env,
            "ciphertext argument missing or not a Uint8Array",
        ))?;
        let sk_arr: Uint8Array = info.arg(1).ok_or(js_err(
            env,
            "secretKey argument missing or not a Uint8Array",
        ))?;

        let plaintext = match spark_frost::bridge::decrypt_ecies(
            ct_arr.as_slice().to_vec(),
            sk_arr.as_slice().to_vec(),
        ) {
            Ok(p) => p,
            Err(e) => return Err(js_err(env, &format!("decrypt error: {}", e))),
        };

        let js_plaintext = Uint8Array::new(env, plaintext.len())?;
        js_plaintext.as_mut_slice().copy_from_slice(&plaintext);
        Ok(js_plaintext.into())
    })
    .unwrap();

    exports
        .set_named_property("decryptEcies", decrypt_ecies_fn)
        .unwrap();

    // signFrost(msg, keyPackage, nonce, selfCommitment, statechainCommitments?, adaptorPubKey?)
    let sign_frost_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        // msg
        let msg_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "msg argument missing"))?;

        // keyPackage
        let kp_obj: Object = info
            .arg(1)
            .ok_or(js_err(env, "keyPackage argument missing"))?;
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
        let self_commit_obj: Object = info
            .arg(3)
            .ok_or(js_err(env, "selfCommitment argument missing"))?;
        let self_commit_proto = js_commitment_to_proto(env, &self_commit_obj)?;

        // commitments array arg4: [[key, JsCommitment], ...]
        let commit_arr: Array = info
            .arg(4)
            .ok_or(js_err(env, "commitments argument missing"))?;
        let commitments_proto = js_pairs_to_map(env, &commit_arr, |env, val| {
            let obj: Object = val.into();
            js_commitment_to_proto(env, &obj)
        })?;

        // adaptor public key (optional): Uint8Array
        let adaptor_pk: Option<Vec<u8>> = info
            .arg(5)
            .map(|a: Uint8Array| a.as_slice().to_vec())
            .filter(|v| !v.is_empty());

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
    })
    .unwrap();

    exports
        .set_named_property("signFrost", sign_frost_fn)
        .unwrap();

    // aggregateFrost(msg, statechainCommitments, selfCommitment, statechainSignatures, selfSignature, statechainPublicKeys, selfPublicKey, verifyingKey, adaptorPublicKey)
    let aggregate_frost_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        // msg arg0: Uint8Array
        let msg_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "msg argument missing"))?;

        // statechainCommitments arg1: [[id, JsCommitment], ...]
        let comm_arr: Array = info
            .arg(1)
            .ok_or(js_err(env, "statechainCommitments arg missing"))?;
        let commitments_proto = js_pairs_to_map(env, &comm_arr, |env, val| {
            let obj: Object = val.into();
            js_commitment_to_proto(env, &obj)
        })?;

        // selfCommitment arg2: JsCommitment
        let self_commit_obj: Object = info
            .arg(2)
            .ok_or(js_err(env, "selfCommitment arg missing"))?;
        let self_commit_proto = js_commitment_to_proto(env, &self_commit_obj)?;

        // statechainSignatures arg3: [[id, Uint8Array], ...]
        let sig_arr: Array = info
            .arg(3)
            .ok_or(js_err(env, "statechainSignatures arg missing"))?;
        let statechain_signatures = js_pairs_to_map(env, &sig_arr, |_env, val| {
            let ua: Uint8Array = val.into();
            Ok(ua.as_slice().to_vec())
        })?;

        // selfSignature arg4: Uint8Array
        let self_signature: Uint8Array = info
            .arg(4)
            .ok_or(js_err(env, "selfSignature arg missing"))?;
        let self_signature_bytes = self_signature.as_slice().to_vec();

        // statechainPublicKeys arg5: [[id, Uint8Array], ...]
        let pk_arr: Array = info
            .arg(5)
            .ok_or(js_err(env, "statechainPublicKeys arg missing"))?;
        let statechain_public_keys = js_pairs_to_map(env, &pk_arr, |_env, val| {
            let ua: Uint8Array = val.into();
            Ok(ua.as_slice().to_vec())
        })?;

        // selfPublicKey arg6: Uint8Array
        let self_public_key: Uint8Array = info
            .arg(6)
            .ok_or(js_err(env, "selfPublicKey arg missing"))?;
        let public_key = self_public_key.as_slice().to_vec();

        // verifyingKey arg7: Uint8Array
        let verifying_key_arr: Uint8Array =
            info.arg(7).ok_or(js_err(env, "verifyingKey arg missing"))?;
        let verifying_key = verifying_key_arr.as_slice().to_vec();

        // adaptorPublicKey arg8: Uint8Array (optional)
        let adaptor_pk: Option<Vec<u8>> = info
            .arg(8)
            .map(|a: Uint8Array| a.as_slice().to_vec())
            .filter(|v| !v.is_empty());

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
    })
    .unwrap();

    exports
        .set_named_property("aggregateFrost", aggregate_frost_fn)
        .unwrap();

    // splitSecretWithProofs(secret: Uint8Array, threshold: number, numShares: number) -> Array<{ threshold, index, share, proofs }>
    let split_secret_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let secret_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "secret argument missing"))?;
        let threshold_num: Number = info
            .arg(1)
            .ok_or(js_err(env, "threshold argument missing"))?;
        let num_shares_num: Number = info
            .arg(2)
            .ok_or(js_err(env, "numShares argument missing"))?;
        let threshold = u32::from(threshold_num);
        let num_shares = u32::from(num_shares_num);

        let shares = spark_frost::vss::split_secret_with_proofs(
            secret_arr.as_slice(),
            threshold as usize,
            num_shares as usize,
        )
        .map_err(|e| js_err(env, &format!("split_secret_with_proofs error: {e}")))?;

        let mut result = Array::new(env, shares.len())?;
        for (i, vss) in shares.iter().enumerate() {
            let mut obj = Object::new(env)?;

            let threshold_val: Value = Number::with_u32(env, vss.share.threshold as u32).into();
            obj.set_named_property("threshold", threshold_val)?;

            let index_val: Value = Number::with_u32(env, vss.share.index).into();
            obj.set_named_property("index", index_val)?;

            let share_arr = Uint8Array::new(env, vss.share.share.len())?;
            share_arr.as_mut_slice().copy_from_slice(&vss.share.share);
            obj.set_named_property("share", share_arr)?;

            let mut proofs_arr = Array::new(env, vss.proofs.len())?;
            for (j, proof) in vss.proofs.iter().enumerate() {
                let proof_arr = Uint8Array::new(env, proof.len())?;
                proof_arr.as_mut_slice().copy_from_slice(proof);
                proofs_arr.set(j as u32, proof_arr)?;
            }
            obj.set_named_property("proofs", proofs_arr)?;

            result.set(i as u32, obj)?;
        }
        Ok(result.into())
    })
    .unwrap();

    exports
        .set_named_property("splitSecretWithProofs", split_secret_fn)
        .unwrap();

    // recoverSecret(shares: Array<{ threshold, index, share }>) -> Uint8Array
    let recover_secret_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let shares_arr: Array = info.arg(0).ok_or(js_err(env, "shares argument missing"))?;

        let mut shares = Vec::new();
        for i in 0..shares_arr.len() {
            let obj: Object = shares_arr.get(i)?;
            let threshold_num: Number = obj
                .get_named_property("threshold")
                .map_err(|_| js_err(env, "missing threshold field"))?;
            let index_num: Number = obj
                .get_named_property("index")
                .map_err(|_| js_err(env, "missing index field"))?;
            let share_bytes = get_uint8_vec(env, &obj, "share")?;

            shares.push(spark_frost::vss::SecretShare {
                threshold: u32::from(threshold_num) as usize,
                index: u32::from(index_num),
                share: share_bytes,
            });
        }

        let secret = spark_frost::vss::recover_secret(&shares)
            .map_err(|e| js_err(env, &format!("recover_secret error: {e}")))?;

        let js_secret = Uint8Array::new(env, secret.len())?;
        js_secret.as_mut_slice().copy_from_slice(&secret);
        Ok(js_secret.into())
    })
    .unwrap();

    exports
        .set_named_property("recoverSecret", recover_secret_fn)
        .unwrap();

    // validateShare(share: Uint8Array, index: number, threshold: number, proofs: Array<Uint8Array>) -> void
    let validate_share_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let share_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "share argument missing"))?;
        let index_num: Number = info.arg(1).ok_or(js_err(env, "index argument missing"))?;
        let threshold_num: Number = info
            .arg(2)
            .ok_or(js_err(env, "threshold argument missing"))?;
        let proofs_arr: Array = info.arg(3).ok_or(js_err(env, "proofs argument missing"))?;

        let mut proofs = Vec::new();
        for i in 0..proofs_arr.len() {
            let proof: Uint8Array = proofs_arr.get(i)?;
            proofs.push(proof.as_slice().to_vec());
        }

        spark_frost::vss::validate_share(
            share_arr.as_slice(),
            u32::from(index_num),
            u32::from(threshold_num) as usize,
            &proofs,
        )
        .map_err(|e| js_err(env, &format!("validate_share error: {e}")))?;

        Ok(Undefined::new(env).into())
    })
    .unwrap();

    exports
        .set_named_property("validateShare", validate_share_fn)
        .unwrap();

    // constructNodeTxPair(parentTx: Uint8Array, vout: number, address: string, sequence: number, directSequence: number, feeSats: bigint)
    //   -> { cpfp: { tx: Uint8Array }, direct: { tx: Uint8Array } }
    let construct_node_tx_pair_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let parent_tx: Uint8Array = info
            .arg(0)
            .ok_or(js_err(env, "parentTx argument missing"))?;
        let vout_num: Number = info.arg(1).ok_or(js_err(env, "vout argument missing"))?;
        let address_str: String = info.arg(2).ok_or(js_err(env, "address argument missing"))?;
        let sequence_num: Number = info
            .arg(3)
            .ok_or(js_err(env, "sequence argument missing"))?;
        let direct_sequence_num: Number = info
            .arg(4)
            .ok_or(js_err(env, "directSequence argument missing"))?;
        let fee_sats_bi: BigInt = info.arg(5).ok_or(js_err(env, "feeSats argument missing"))?;

        let address: std::string::String = address_str.into();
        let vout = u32::from(vout_num);
        let sequence = u32::from(sequence_num);
        let direct_sequence = u32::from(direct_sequence_num);
        let fee_sats = u64::from(fee_sats_bi);

        let result = construct_node_tx_pair(
            parent_tx.as_slice(),
            vout,
            &address,
            sequence,
            direct_sequence,
            fee_sats,
        )
        .map_err(|e| js_err(env, &format!("construct_node_tx_pair error: {e}")))?;

        let mut cpfp_obj = Object::new(env)?;
        let cpfp_tx = Uint8Array::new(env, result.cpfp.tx_bytes.len())?;
        cpfp_tx
            .as_mut_slice()
            .copy_from_slice(&result.cpfp.tx_bytes);
        cpfp_obj.set_named_property("tx", cpfp_tx)?;

        let mut direct_obj = Object::new(env)?;
        let direct_tx = Uint8Array::new(env, result.direct.tx_bytes.len())?;
        direct_tx
            .as_mut_slice()
            .copy_from_slice(&result.direct.tx_bytes);
        direct_obj.set_named_property("tx", direct_tx)?;

        let mut out = Object::new(env)?;
        out.set_named_property("cpfp", cpfp_obj)?;
        out.set_named_property("direct", direct_obj)?;
        Ok(out.into())
    })
    .unwrap();

    exports
        .set_named_property("constructNodeTxPair", construct_node_tx_pair_fn)
        .unwrap();

    // constructRefundTxTrio(cpfpNodeTx: Uint8Array, directNodeTx: Uint8Array|null, vout: number,
    //   receivingPubkey: Uint8Array, network: string, sequence: number, directSequence: number, feeSats: bigint)
    //   -> { cpfp_refund: { tx }, direct_refund?: { tx }, direct_from_cpfp_refund: { tx } }
    let construct_refund_tx_trio_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let cpfp_node_tx: Uint8Array = info
            .arg(0)
            .ok_or(js_err(env, "cpfpNodeTx argument missing"))?;
        // directNodeTx can be null/undefined — bare_rust's arg() doesn't distinguish
        // null from a valid value, so we filter out empty vecs (a valid tx is never 0 bytes).
        let direct_node_tx_opt: Option<Vec<u8>> = info
            .arg(1)
            .map(|a: Uint8Array| a.as_slice().to_vec())
            .filter(|v| !v.is_empty());
        let vout_num: Number = info.arg(2).ok_or(js_err(env, "vout argument missing"))?;
        let receiving_pubkey: Uint8Array = info
            .arg(3)
            .ok_or(js_err(env, "receivingPubkey argument missing"))?;
        let network_str: String = info.arg(4).ok_or(js_err(env, "network argument missing"))?;
        let sequence_num: Number = info
            .arg(5)
            .ok_or(js_err(env, "sequence argument missing"))?;
        let direct_sequence_num: Number = info
            .arg(6)
            .ok_or(js_err(env, "directSequence argument missing"))?;
        let fee_sats_bi: BigInt = info.arg(7).ok_or(js_err(env, "feeSats argument missing"))?;

        let network: std::string::String = network_str.into();
        let vout = u32::from(vout_num);
        let sequence = u32::from(sequence_num);
        let direct_sequence = u32::from(direct_sequence_num);
        let fee_sats = u64::from(fee_sats_bi);

        let result = construct_refund_tx_trio(
            cpfp_node_tx.as_slice(),
            direct_node_tx_opt.as_deref(),
            vout,
            receiving_pubkey.as_slice(),
            &network,
            sequence,
            direct_sequence,
            fee_sats,
        )
        .map_err(|e| js_err(env, &format!("construct_refund_tx_trio error: {e}")))?;

        let mut cpfp_refund_obj = Object::new(env)?;
        let cpfp_refund_tx = Uint8Array::new(env, result.cpfp_refund.tx_bytes.len())?;
        cpfp_refund_tx
            .as_mut_slice()
            .copy_from_slice(&result.cpfp_refund.tx_bytes);
        cpfp_refund_obj.set_named_property("tx", cpfp_refund_tx)?;

        let mut out = Object::new(env)?;
        out.set_named_property("cpfp_refund", cpfp_refund_obj)?;

        if let Some(ref dr) = result.direct_refund {
            let mut direct_refund_obj = Object::new(env)?;
            let dr_tx = Uint8Array::new(env, dr.tx_bytes.len())?;
            dr_tx.as_mut_slice().copy_from_slice(&dr.tx_bytes);
            direct_refund_obj.set_named_property("tx", dr_tx)?;
            out.set_named_property("direct_refund", direct_refund_obj)?;
        }

        let mut dcr_obj = Object::new(env)?;
        let dcr_tx = Uint8Array::new(env, result.direct_from_cpfp_refund.tx_bytes.len())?;
        dcr_tx
            .as_mut_slice()
            .copy_from_slice(&result.direct_from_cpfp_refund.tx_bytes);
        dcr_obj.set_named_property("tx", dcr_tx)?;
        out.set_named_property("direct_from_cpfp_refund", dcr_obj)?;

        Ok(out.into())
    })
    .unwrap();

    exports
        .set_named_property("constructRefundTxTrio", construct_refund_tx_trio_fn)
        .unwrap();

    // computeMultiInputSighash(tx: Uint8Array, inputIndex: number, prevOutScripts: Array<Uint8Array>, prevOutValues: Array<number>)
    //   -> Uint8Array
    let compute_multi_input_sighash_fn = Function::new(&env, |env, info| -> Result<Value, Value> {
        let tx_arr: Uint8Array = info.arg(0).ok_or(js_err(env, "tx argument missing"))?;
        let input_index_num: Number = info
            .arg(1)
            .ok_or(js_err(env, "inputIndex argument missing"))?;
        let scripts_arr: Array = info
            .arg(2)
            .ok_or(js_err(env, "prevOutScripts argument missing"))?;
        let values_arr: Array = info
            .arg(3)
            .ok_or(js_err(env, "prevOutValues argument missing"))?;

        let input_index = u32::from(input_index_num);

        let mut prev_out_scripts: Vec<Vec<u8>> = Vec::new();
        for i in 0..scripts_arr.len() {
            let script: Uint8Array = scripts_arr.get(i)?;
            prev_out_scripts.push(script.as_slice().to_vec());
        }

        let mut prev_out_values: Vec<u64> = Vec::new();
        for i in 0..values_arr.len() {
            let val: Number = values_arr.get(i)?;
            prev_out_values.push(i64::from(val) as u64);
        }

        let result = compute_multi_input_sighash(
            tx_arr.as_slice(),
            input_index,
            &prev_out_scripts,
            &prev_out_values,
        )
        .map_err(|e| js_err(env, &format!("compute_multi_input_sighash error: {e}")))?;

        let js_result = Uint8Array::new(env, result.len())?;
        js_result.as_mut_slice().copy_from_slice(&result);
        Ok(js_result.into())
    })
    .unwrap();

    exports
        .set_named_property("computeMultiInputSighash", compute_multi_input_sighash_fn)
        .unwrap();

    exports.into()
}
