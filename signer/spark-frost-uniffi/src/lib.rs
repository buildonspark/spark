uniffi::include_scaffolding!("spark_frost");

use std::io::Write;
use std::{collections::HashMap, fs::OpenOptions, str::FromStr};

use bitcoin::{
    absolute::LockTime,
    consensus::deserialize,
    hashes::Hash,
    key::Secp256k1,
    sighash::{Prevouts, SighashCache},
    transaction::Version,
    Address, Amount, OutPoint, ScriptBuf, Sequence, TapSighashType, Transaction, TxIn, TxOut, Txid,
    Witness,
};
use ecies::{decrypt, encrypt};
use frost_secp256k1_tr::Identifier;
use gloo_utils::format::JsValueSerdeExt;
use serde::{Deserialize, Serialize};
use wasm_bindgen::prelude::*;

/// A uniffi library for the Spark Frost signing protocol on client side.
/// This only signs as the required participant in the signing protocol.
///
#[derive(Debug, Clone)]
pub enum Error {
    Spark(String),
}

impl From<String> for Error {
    fn from(s: String) -> Self {
        Error::Spark(s)
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self)
    }
}
impl Into<JsValue> for Error {
    fn into(self) -> JsValue {
        JsValue::from_str(&self.to_string())
    }
}

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct SigningNonce {
    #[wasm_bindgen(getter_with_clone)]
    pub hiding: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub binding: Vec<u8>,
}

#[wasm_bindgen]
impl SigningNonce {
    #[wasm_bindgen(constructor)]
    pub fn new(hiding: Vec<u8>, binding: Vec<u8>) -> Self {
        SigningNonce { hiding, binding }
    }
}

impl Into<spark_frost::proto::frost::SigningNonce> for SigningNonce {
    fn into(self) -> spark_frost::proto::frost::SigningNonce {
        spark_frost::proto::frost::SigningNonce {
            hiding: self.hiding,
            binding: self.binding,
        }
    }
}

impl From<spark_frost::proto::frost::SigningNonce> for SigningNonce {
    fn from(proto: spark_frost::proto::frost::SigningNonce) -> Self {
        SigningNonce {
            hiding: proto.hiding,
            binding: proto.binding,
        }
    }
}

#[wasm_bindgen]
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SigningCommitment {
    #[wasm_bindgen(getter_with_clone)]
    pub hiding: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub binding: Vec<u8>,
}

#[wasm_bindgen]
impl SigningCommitment {
    #[wasm_bindgen(constructor)]
    pub fn new(hiding: Vec<u8>, binding: Vec<u8>) -> Self {
        SigningCommitment { hiding, binding }
    }
}

impl Into<spark_frost::proto::common::SigningCommitment> for SigningCommitment {
    fn into(self) -> spark_frost::proto::common::SigningCommitment {
        spark_frost::proto::common::SigningCommitment {
            hiding: self.hiding,
            binding: self.binding,
        }
    }
}

impl From<spark_frost::proto::common::SigningCommitment> for SigningCommitment {
    fn from(proto: spark_frost::proto::common::SigningCommitment) -> Self {
        SigningCommitment {
            hiding: proto.hiding,
            binding: proto.binding,
        }
    }
}

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct NonceResult {
    #[wasm_bindgen(getter_with_clone)]
    pub nonce: SigningNonce,
    #[wasm_bindgen(getter_with_clone)]
    pub commitment: SigningCommitment,
}

impl From<spark_frost::proto::frost::SigningNonceResult> for NonceResult {
    fn from(proto: spark_frost::proto::frost::SigningNonceResult) -> Self {
        NonceResult {
            nonce: proto.nonces.clone().expect("No nonce").into(),
            commitment: proto.commitments.clone().expect("No commitment").into(),
        }
    }
}

#[wasm_bindgen(js_name = KeyPackage)]
#[derive(Debug, Clone)]
pub struct KeyPackage {
    // The secret key for the user
    #[wasm_bindgen(getter_with_clone)]
    pub secret_key: Vec<u8>,
    // The public key for the user
    #[wasm_bindgen(getter_with_clone)]
    pub public_key: Vec<u8>,
    // The verifying key for the user + SE
    #[wasm_bindgen(getter_with_clone)]
    pub verifying_key: Vec<u8>,
}

#[wasm_bindgen]
impl KeyPackage {
    #[wasm_bindgen(constructor)]
    pub fn new(secret_key: Vec<u8>, public_key: Vec<u8>, verifying_key: Vec<u8>) -> KeyPackage {
        Self {
            secret_key,
            public_key,
            verifying_key,
        }
    }
}

impl Into<spark_frost::proto::frost::KeyPackage> for KeyPackage {
    fn into(self) -> spark_frost::proto::frost::KeyPackage {
        let user_identifier =
            Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");
        let user_identifier_string = hex::encode(user_identifier.to_scalar().to_bytes());
        spark_frost::proto::frost::KeyPackage {
            identifier: user_identifier_string.clone(),
            secret_share: self.secret_key.clone(),
            public_shares: HashMap::from([(
                user_identifier_string.clone(),
                self.public_key.clone(),
            )]),
            public_key: self.verifying_key.clone(),
            min_signers: 1,
        }
    }
}

#[wasm_bindgen]
pub fn frost_nonce(key_package: KeyPackage) -> Result<NonceResult, Error> {
    let key_package_proto: spark_frost::proto::frost::KeyPackage = key_package.into();
    let request = spark_frost::proto::frost::FrostNonceRequest {
        key_packages: vec![key_package_proto],
    };
    let response = spark_frost::signing::frost_nonce(&request).map_err(|e| Error::Spark(e))?;
    let nonce = response
        .results
        .first()
        .ok_or(Error::Spark("No nonce generated".to_owned()))?
        .clone();
    Ok(nonce.into())
}

pub fn sign_frost(
    msg: Vec<u8>,
    key_package: KeyPackage,
    nonce: SigningNonce,
    self_commitment: SigningCommitment,
    statechain_commitments: HashMap<String, SigningCommitment>,
) -> Result<Vec<u8>, Error> {
    log_to_file("Entering sign_frost");
    let signing_job = spark_frost::proto::frost::FrostSigningJob {
        job_id: uuid::Uuid::new_v4().to_string(),
        message: msg,
        key_package: Some(key_package.clone().into()),
        nonce: Some(nonce.into()),
        user_commitments: Some(self_commitment.into()),
        verifying_key: key_package.clone().verifying_key.clone(),
        commitments: statechain_commitments
            .into_iter()
            .map(|(k, v)| (k, v.into()))
            .collect(),
    };
    let request = spark_frost::proto::frost::SignFrostRequest {
        signing_jobs: vec![signing_job],
        role: spark_frost::proto::frost::SigningRole::User.into(),
    };
    let response = spark_frost::signing::sign_frost(&request).map_err(|e| Error::Spark(e))?;
    let result = response
        .results
        .iter()
        .next()
        .ok_or(Error::Spark("No result".to_owned()))?
        .1;
    Ok(result.signature_share.clone())
}

#[wasm_bindgen]
pub fn wasm_sign_frost(
    msg: Vec<u8>,
    key_package: KeyPackage,
    nonce: SigningNonce,
    self_commitment: SigningCommitment,
    statechain_commitments: JsValue,
) -> Result<Vec<u8>, Error> {
    let statechain_commitments: HashMap<String, SigningCommitment> =
        JsValue::into_serde(&statechain_commitments).map_err(|e| Error::Spark(e.to_string()))?;
    sign_frost(msg, key_package, nonce, self_commitment, statechain_commitments)
}

pub fn aggregate_frost(
    msg: Vec<u8>,
    statechain_commitments: HashMap<String, SigningCommitment>,
    self_commitment: SigningCommitment,
    statechain_signatures: HashMap<String, Vec<u8>>,
    self_signature: Vec<u8>,
    statechain_public_keys: HashMap<String, Vec<u8>>,
    self_public_key: Vec<u8>,
    verifying_key: Vec<u8>,
) -> Result<Vec<u8>, Error> {
    log_to_file("Entering aggregate_frost");
    let request = spark_frost::proto::frost::AggregateFrostRequest {
        message: msg,
        commitments: statechain_commitments
            .into_iter()
            .map(|(k, v)| (k, v.into()))
            .collect(),
        user_commitments: Some(self_commitment.into()),
        user_public_key: self_public_key.clone(),
        signature_shares: statechain_signatures
            .into_iter()
            .map(|(k, v)| (k, v.clone()))
            .collect(),
        public_shares: statechain_public_keys
            .into_iter()
            .map(|(k, v)| (k, v.clone()))
            .collect(),
        verifying_key: verifying_key.clone(),
        user_signature_share: self_signature.clone(),
    };
    log_to_file("Aggregating frost protobuf constructed");
    let response = spark_frost::signing::aggregate_frost(&request).map_err(|e| Error::Spark(e))?;
    log_to_file("Aggregating frost response received");
    Ok(response.signature)
}

#[wasm_bindgen]
pub fn wasm_aggregate_frost(
    msg: Vec<u8>,
    statechain_commitments: JsValue,
    self_commitment: SigningCommitment,
    statechain_signatures: JsValue,
    self_signature: Vec<u8>,
    statechain_public_keys: JsValue,
    self_public_key: Vec<u8>,
    verifying_key: Vec<u8>,
) -> Result<Vec<u8>, Error> {
    let statechain_commitments: HashMap<String, SigningCommitment> =
        JsValue::into_serde(&statechain_commitments).map_err(|e| Error::Spark(e.to_string()))?;
    let statechain_signatures: HashMap<String, Vec<u8>> =
        JsValue::into_serde(&statechain_signatures).map_err(|e| Error::Spark(e.to_string()))?;
    let statechain_public_keys: HashMap<String, Vec<u8>> =
        JsValue::into_serde(&statechain_public_keys).map_err(|e| Error::Spark(e.to_string()))?;
    aggregate_frost(msg, statechain_commitments, self_commitment, statechain_signatures, self_signature, statechain_public_keys, self_public_key, verifying_key)
}

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct TransactionResult {
    #[wasm_bindgen(getter_with_clone)]
    pub tx: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub sighash: Vec<u8>,
}


// Construct a tx that pays from the tx.out[vout] to the address.
#[wasm_bindgen]
pub fn construct_node_tx(
    tx: Vec<u8>,
    vout: u32,
    address: String,
    locktime: u16,
) -> Result<TransactionResult, Error> {
    // Decode the input transaction
    let prev_tx: Transaction = deserialize(&tx).map_err(|e| Error::Spark(e.to_string()))?;

    // Verify that vout index is valid
    if vout as usize >= prev_tx.output.len() {
        return Err(Error::Spark("Invalid vout index".to_owned()));
    }

    // Get the previous output we'll be spending
    let prev_output = &prev_tx.output[vout as usize];
    let prev_amount = prev_output.value;

    // Create the outpoint (reference to the UTXO we're spending)
    let outpoint = OutPoint::new(prev_tx.compute_txid(), vout);

    // Create the input
    let input = TxIn {
        previous_output: outpoint,
        script_sig: ScriptBuf::new(), // Empty for now, will be filled by the signing process
        sequence: Sequence::from_height(locktime), // Default sequence number
        witness: Witness::new(),      // Empty witness for now
    };

    let dest_address = Address::from_str(&address)
        .map_err(|e| Error::Spark(e.to_string()))?
        .assume_checked();

    // Create the P2TR output
    let output = TxOut {
        value: prev_amount,
        script_pubkey: dest_address.script_pubkey(),
    };

    // Construct the transaction with version 2 for Taproot support
    let new_tx = Transaction {
        version: Version::TWO,
        lock_time: LockTime::from_height(locktime as u32).unwrap(),
        input: vec![input],
        output: vec![output],
    };

    let sighash = SighashCache::new(&new_tx)
        .taproot_key_spend_signature_hash(
            0,
            &Prevouts::All(&[prev_output]),
            TapSighashType::Default,
        )
        .unwrap();

    // Serialize the transaction
    Ok(TransactionResult {
        tx: bitcoin::consensus::serialize(&new_tx),
        sighash: sighash.as_raw_hash().to_byte_array().to_vec(),
    })
}

// Construct a tx that pays from the tx.out[vout] to the address.
#[wasm_bindgen]
pub fn construct_refund_tx(
    tx: Vec<u8>,
    vout: u32,
    pubkey: Vec<u8>,
    network: String,
    locktime: u16,
) -> Result<TransactionResult, Error> {
    // Decode the input transaction
    let prev_tx: Transaction = deserialize(&tx).map_err(|e| Error::Spark(e.to_string()))?;

    // Verify that vout index is valid
    if vout as usize >= prev_tx.output.len() {
        return Err(Error::Spark("Invalid vout index".to_owned()));
    }

    // Get the previous output we'll be spending
    let prev_output = &prev_tx.output[vout as usize];
    let prev_amount = prev_output.value;

    // Create the outpoint (reference to the UTXO we're spending)
    let outpoint = OutPoint::new(prev_tx.compute_txid(), vout);

    // Create the input
    let input = TxIn {
        previous_output: outpoint,
        script_sig: ScriptBuf::new(), // Empty for now, will be filled by the signing process
        sequence: Sequence::from_height(locktime), // Default sequence number
        witness: Witness::new(),      // Empty witness for now
    };

    let x_only_key = {
        let full_key =
            bitcoin::PublicKey::from_slice(&pubkey).map_err(|e| Error::Spark(e.to_string()))?;
        full_key.inner.x_only_public_key().0
    };

    let network = match network.as_str() {
        "mainnet" => bitcoin::Network::Bitcoin,
        "testnet" => bitcoin::Network::Testnet,
        "signet" => bitcoin::Network::Signet,
        "regtest" => bitcoin::Network::Regtest,
        _ => return Err(Error::Spark("Invalid network".to_owned())),
    };

    let secp = Secp256k1::new();

    let p2tr_address = bitcoin::Address::p2tr(&secp, x_only_key, None, network);

    // Create the P2TR output
    let output = TxOut {
        value: prev_amount,
        script_pubkey: p2tr_address.script_pubkey(),
    };

    // Construct the transaction with version 2 for Taproot support
    let new_tx = Transaction {
        version: Version::TWO,
        lock_time: LockTime::from_height(locktime as u32).unwrap(),
        input: vec![input],
        output: vec![output],
    };

    let sighash = SighashCache::new(&new_tx)
        .taproot_key_spend_signature_hash(
            0,
            &Prevouts::All(&[prev_output]),
            TapSighashType::Default,
        )
        .unwrap();

    // Serialize the transaction
    Ok(TransactionResult {
        tx: bitcoin::consensus::serialize(&new_tx),
        sighash: sighash.as_raw_hash().to_byte_array().to_vec(),
    })
}

// Construct a tx that pays from the tx.out[vout] to the address.
#[wasm_bindgen]
pub fn construct_split_tx(
    tx: Vec<u8>,
    vout: u32,
    addresses: Vec<String>,
    locktime: u16,
) -> Result<TransactionResult, Error> {
    // Decode the input transaction
    let prev_tx: Transaction = deserialize(&tx).map_err(|e| Error::Spark(e.to_string()))?;

    // Verify that vout index is valid
    if vout as usize >= prev_tx.output.len() {
        return Err(Error::Spark("Invalid vout index".to_owned()));
    }

    // Get the previous output we'll be spending
    let prev_output = &prev_tx.output[vout as usize];
    let prev_amount = prev_output.value;

    // Create the outpoint (reference to the UTXO we're spending)
    let outpoint = OutPoint::new(prev_tx.compute_txid(), vout);

    // Create the input
    let input = TxIn {
        previous_output: outpoint,
        script_sig: ScriptBuf::new(), // Empty for now, will be filled by the signing process
        sequence: Sequence::from_height(locktime), // Default sequence number
        witness: Witness::new(),      // Empty witness for now
    };

    let mut outputs = vec![];

    for address in addresses {
        let dest_address = Address::from_str(&address)
            .map_err(|e| Error::Spark(e.to_string()))?
            .assume_checked();

        outputs.push(TxOut {
            value: prev_amount,
            script_pubkey: dest_address.script_pubkey(),
        });
    }

    // Construct the transaction with version 2 for Taproot support
    let new_tx = Transaction {
        version: Version::TWO,
        lock_time: LockTime::from_height(locktime as u32).unwrap(),
        input: vec![input],
        output: outputs,
    };

    let sighash = SighashCache::new(&new_tx)
        .taproot_key_spend_signature_hash(
            0,
            &Prevouts::All(&[prev_output]),
            TapSighashType::Default,
        )
        .unwrap();

    // Serialize the transaction
    Ok(TransactionResult {
        tx: bitcoin::consensus::serialize(&new_tx),
        sighash: sighash.as_raw_hash().to_byte_array().to_vec(),
    })
}

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct DummyTx {
    #[wasm_bindgen(getter_with_clone)]
    pub tx: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub txid: String,
}

#[wasm_bindgen]
pub fn create_dummy_tx(address: String, amount_sats: u64) -> Result<DummyTx, Error> {
    // Create the input
    let input = TxIn {
        previous_output: OutPoint {
            txid: Txid::from_slice(&[0; 32]).unwrap(),
            vout: 0,
        },
        script_sig: ScriptBuf::new(), // Empty for now, will be filled by the signing process
        sequence: Sequence::from_height(0), // Default sequence number
        witness: Witness::new(),      // Empty witness for now
    };

    let dest_address = Address::from_str(&address)
        .map_err(|e| Error::Spark(e.to_string()))?
        .assume_checked();

    // Create the P2TR output
    let output = TxOut {
        value: Amount::from_sat(amount_sats),
        script_pubkey: dest_address.script_pubkey(),
    };

    // Construct the transaction with version 2 for Taproot support
    let new_tx = Transaction {
        version: Version::TWO,
        lock_time: LockTime::from_height(0).unwrap(),
        input: vec![input],
        output: vec![output],
    };

    Ok(DummyTx {
        tx: bitcoin::consensus::serialize(&new_tx),
        txid: new_tx.compute_txid().to_string(),
    })
}

fn log_to_file(message: &str) {
    if let Ok(mut file) = OpenOptions::new()
        .create(true)
        .append(true)
        .open("/Users/zhenlu/rust.log")
    {
        writeln!(file, "{}", message).ok();
    }
}

#[wasm_bindgen]
pub fn encrypt_ecies(msg: Vec<u8>, public_key_bytes: Vec<u8>) -> Result<Vec<u8>, Error> {
    encrypt(&public_key_bytes, &msg).map_err(|e| Error::Spark(e.to_string()))
}

#[wasm_bindgen]
pub fn decrypt_ecies(encrypted_msg: Vec<u8>, private_key_bytes: Vec<u8>) -> Result<Vec<u8>, Error> {
    decrypt(&private_key_bytes, &encrypted_msg).map_err(|e| Error::Spark(e.to_string()))
}
