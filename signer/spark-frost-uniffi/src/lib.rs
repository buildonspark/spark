// Uniffi generates code with bad comments for some reason...
#![allow(clippy::empty_line_after_doc_comments)]
uniffi::include_scaffolding!("spark_frost");

use std::io::Write;
use std::{collections::HashMap, fs::OpenOptions, str::FromStr};

use bitcoin::{
    absolute::LockTime,
    consensus::deserialize,
    hashes::Hash,
    key::Parity,
    key::{Secp256k1, TapTweak},
    relative,
    secp256k1::{ecdsa::Signature, rand, Message, PublicKey, SecretKey},
    sighash::{Prevouts, SighashCache},
    transaction::Version,
    Address, Amount, OutPoint, ScriptBuf, Sequence, TapSighashType, Transaction, TxIn, TxOut,
    Witness,
};
use frost_secp256k1_tr::Identifier;
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
        match self {
            Error::Spark(s) => write!(f, "Spark error: {s}"),
        }
    }
}

impl From<Error> for JsValue {
    fn from(val: Error) -> Self {
        JsValue::from_str(&format!("{val}"))
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
    pub fn new(hiding: Vec<u8>, binding: Vec<u8>) -> SigningNonce {
        SigningNonce { hiding, binding }
    }
}

impl From<SigningNonce> for spark_frost::proto::frost::SigningNonce {
    fn from(val: SigningNonce) -> Self {
        spark_frost::proto::frost::SigningNonce {
            hiding: val.hiding,
            binding: val.binding,
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

impl From<SigningCommitment> for spark_frost::proto::common::SigningCommitment {
    fn from(val: SigningCommitment) -> Self {
        spark_frost::proto::common::SigningCommitment {
            hiding: val.hiding,
            binding: val.binding,
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

#[wasm_bindgen(getter_with_clone)]
#[derive(Debug, Clone)]
pub struct KeyPackage {
    // The secret key for the user
    pub secret_key: Vec<u8>,
    // The public key for the user
    pub public_key: Vec<u8>,
    // The verifying key for the user + SE
    pub verifying_key: Vec<u8>,
}

#[wasm_bindgen]
impl KeyPackage {
    #[wasm_bindgen(constructor)]
    pub fn new(secret_key: Vec<u8>, public_key: Vec<u8>, verifying_key: Vec<u8>) -> KeyPackage {
        KeyPackage {
            secret_key,
            public_key,
            verifying_key,
        }
    }
}

impl From<KeyPackage> for spark_frost::proto::frost::KeyPackage {
    fn from(val: KeyPackage) -> Self {
        let user_identifier =
            Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");
        let user_identifier_string = hex::encode(user_identifier.to_scalar().to_bytes());
        spark_frost::proto::frost::KeyPackage {
            identifier: user_identifier_string.clone(),
            secret_share: val.secret_key.clone(),
            public_shares: HashMap::from([(
                user_identifier_string.clone(),
                val.public_key.clone(),
            )]),
            public_key: val.verifying_key.clone(),
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
    let response = spark_frost::signing::frost_nonce(&request).map_err(Error::Spark)?;
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
    adaptor_public_key: Option<Vec<u8>>,
) -> Result<Vec<u8>, Error> {
    let proto_commitments: HashMap<_, _> = statechain_commitments
        .into_iter()
        .map(|(k, v)| (k, v.into()))
        .collect();

    spark_frost::bridge::sign_frost(
        msg,
        key_package.clone().into(),
        nonce.into(),
        self_commitment.into(),
        proto_commitments,
        adaptor_public_key,
    )
    .map_err(Error::Spark)
}

#[wasm_bindgen]
pub fn wasm_sign_frost(
    msg: Vec<u8>,
    key_package: KeyPackage,
    nonce: SigningNonce,
    self_commitment: SigningCommitment,
    statechain_commitments: JsValue,
    adaptor_public_key: Option<Vec<u8>>,
) -> Result<Vec<u8>, Error> {
    let statechain_commitments: HashMap<String, SigningCommitment> =
        serde_wasm_bindgen::from_value(statechain_commitments).map_err(|e| {
            log_to_file(&format!("Deserialization error: {e:?}"));
            Error::Spark(format!("Failed to deserialize commitments: {e}"))
        })?;
    sign_frost(
        msg,
        key_package,
        nonce,
        self_commitment,
        statechain_commitments,
        adaptor_public_key,
    )
}

#[allow(clippy::too_many_arguments)]
pub fn aggregate_frost(
    msg: Vec<u8>,
    statechain_commitments: HashMap<String, SigningCommitment>,
    self_commitment: SigningCommitment,
    statechain_signatures: HashMap<String, Vec<u8>>,
    self_signature: Vec<u8>,
    statechain_public_keys: HashMap<String, Vec<u8>>,
    self_public_key: Vec<u8>,
    verifying_key: Vec<u8>,
    adaptor_public_key: Option<Vec<u8>>,
) -> Result<Vec<u8>, Error> {
    log_to_file("Entering aggregate_frost");

    let commitments_proto: HashMap<_, _> = statechain_commitments
        .into_iter()
        .map(|(k, v)| (k, v.into()))
        .collect();

    spark_frost::bridge::aggregate_frost(
        msg,
        commitments_proto,
        self_commitment.into(),
        statechain_signatures,
        self_signature,
        statechain_public_keys,
        self_public_key,
        verifying_key,
        adaptor_public_key,
    )
    .map_err(Error::Spark)
}

pub fn validate_signature_share(
    msg: Vec<u8>,
    statechain_commitments: HashMap<String, SigningCommitment>,
    self_commitment: SigningCommitment,
    signature_share: Vec<u8>,
    public_share: Vec<u8>,
    verifying_key: Vec<u8>,
) -> bool {
    let identifier =
        Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");
    let identifier_string = hex::encode(identifier.to_scalar().to_bytes());
    let request = spark_frost::proto::frost::ValidateSignatureShareRequest {
        message: msg,
        identifier: identifier_string,
        role: spark_frost::proto::frost::SigningRole::User.into(),
        signature_share,
        public_share,
        verifying_key,
        user_commitments: Some(self_commitment.into()),
        commitments: statechain_commitments
            .into_iter()
            .map(|(k, v)| (k, v.into()))
            .collect(),
    };
    spark_frost::signing::validate_signature_share(&request).is_ok()
}

#[wasm_bindgen]
#[allow(clippy::too_many_arguments)]
pub fn wasm_aggregate_frost(
    msg: Vec<u8>,
    statechain_commitments: JsValue,
    self_commitment: SigningCommitment,
    statechain_signatures: JsValue,
    self_signature: Vec<u8>,
    statechain_public_keys: JsValue,
    self_public_key: Vec<u8>,
    verifying_key: Vec<u8>,
    adaptor_public_key: Option<Vec<u8>>,
) -> Result<Vec<u8>, Error> {
    let statechain_commitments: HashMap<String, SigningCommitment> =
        serde_wasm_bindgen::from_value(statechain_commitments)
            .map_err(|e| Error::Spark(e.to_string()))?;
    let statechain_signatures: HashMap<String, Vec<u8>> =
        serde_wasm_bindgen::from_value(statechain_signatures)
            .map_err(|e| Error::Spark(e.to_string()))?;
    let statechain_public_keys: HashMap<String, Vec<u8>> =
        serde_wasm_bindgen::from_value(statechain_public_keys)
            .map_err(|e| Error::Spark(e.to_string()))?;

    aggregate_frost(
        msg,
        statechain_commitments,
        self_commitment,
        statechain_signatures,
        self_signature,
        statechain_public_keys,
        self_public_key,
        verifying_key,
        adaptor_public_key,
    )
}

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct TransactionResult {
    #[wasm_bindgen(getter_with_clone)]
    pub tx: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub sighash: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub inputs: Vec<TxInResult>,
}

/// A stand-in for TxIn.
#[wasm_bindgen(js_name = "TxIn")]
#[derive(Debug, Clone)]
pub struct TxInResult {
    #[wasm_bindgen]
    pub sequence: u32,
}

impl From<TxIn> for TxInResult {
    fn from(value: TxIn) -> Self {
        Self {
            sequence: value.sequence.to_consensus_u32(),
        }
    }
}

impl TxInResult {
    /// Extracts the relative block-based timelock from the transaction input's sequence number.
    pub fn relative_blocks_timelock(&self) -> Result<u16, Error> {
        let seq = Sequence::from_consensus(self.sequence);
        let timelock = seq.to_relative_lock_time().ok_or(Error::Spark(
            "sequence number is not a relative timelock".to_owned(),
        ))?;
        match timelock {
            relative::LockTime::Blocks(height) => Ok(height.value()),
            relative::LockTime::Time(_) => {
                Err(Error::Spark("timelock is not block-based".to_owned()))
            }
        }
    }
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
        sequence: Sequence::from_consensus(u32::from(locktime)), // Set high bit for new sequence format
        witness: Witness::new(),                                 // Empty witness for now
    };

    let dest_address = Address::from_str(&address)
        .map_err(|e| Error::Spark(e.to_string()))?
        .assume_checked();

    // Create the P2TR output
    let output = TxOut {
        value: prev_amount,
        script_pubkey: dest_address.script_pubkey(),
    };

    // Construct the transaction with version 3
    let new_tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input.clone()],
        output: vec![
            output, // Original output
            TxOut {
                // Ephemeral anchor output
                value: Amount::from_sat(0),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51, 0x02, 0x4e, 0x73]), // Pay-to-anchor (P2A) ephemeral anchor output
            },
        ],
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
        inputs: vec![input.into()],
    })
}

// Construct a tx that pays from the tx.out[vout] to the address.
#[wasm_bindgen]
pub fn construct_refund_tx(
    tx: Vec<u8>,
    vout: u32,
    pubkey: Vec<u8>,
    network: String,
    sequence: u32,
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
        sequence: Sequence::from_consensus(sequence), // Set high bit for new sequence format
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

    // Construct the transaction with version 3
    let new_tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input.clone()],
        output: vec![
            output,
            TxOut {
                // Ephemeral anchor output
                value: Amount::from_sat(0),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51, 0x02, 0x4e, 0x73]), // Pay-to-anchor (P2A) ephemeral anchor output
            },
        ],
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
        inputs: vec![input.into()],
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
        sequence: Sequence::from_consensus(u32::from(locktime)), // Set high bit for new sequence format
        witness: Witness::new(),                                 // Empty witness for now
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

    // Construct the transaction with version 3
    let new_tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input.clone()],
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
        inputs: vec![input.into()],
    })
}

// Construct a tx that pays from the tx.out[vout] to the address, without anchor output and with fee deduction.
#[wasm_bindgen]
pub fn construct_direct_refund_tx(
    tx: Vec<u8>,
    vout: u32,
    pubkey: Vec<u8>,
    network: String,
    sequence: u32,
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

    // Calculate fee (191 vbytes * 5 sats/vbyte = 955 sats)
    let fee = Amount::from_sat(191 * 5);

    // If amount is too small to pay fee, don't deduct it
    let output_amount = if prev_amount <= fee {
        prev_amount
    } else {
        prev_amount - fee
    };

    // Create the outpoint (reference to the UTXO we're spending)
    let outpoint = OutPoint::new(prev_tx.compute_txid(), vout);

    // Create the input
    let input = TxIn {
        previous_output: outpoint,
        script_sig: ScriptBuf::new(), // Empty for now, will be filled by the signing process
        sequence: Sequence::from_consensus(sequence), // Set high bit for new sequence format
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

    // Create the P2TR output with fee deducted
    let output = TxOut {
        value: output_amount,
        script_pubkey: p2tr_address.script_pubkey(),
    };

    // Construct the transaction with version 3
    let new_tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input.clone()],
        output: vec![output], // No ephemeral anchor output
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
        inputs: vec![input.into()],
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
    match spark_frost::bridge::create_dummy_tx(&address, amount_sats) {
        Ok(inner) => Ok(DummyTx {
            tx: inner.tx,
            txid: inner.txid,
        }),
        Err(e) => Err(Error::Spark(e)),
    }
}

fn log_to_file(message: &str) {
    if let Ok(mut file) = OpenOptions::new()
        .create(true)
        .append(true)
        .open("/Users/zhenlu/rust.log")
    {
        writeln!(file, "{message}").ok();
    }
}

#[wasm_bindgen]
pub fn encrypt_ecies(msg: Vec<u8>, public_key_bytes: Vec<u8>) -> Result<Vec<u8>, Error> {
    match spark_frost::bridge::encrypt_ecies(&msg, &public_key_bytes) {
        Ok(v) => Ok(v),
        Err(e) => Err(Error::Spark(e)),
    }
}

#[wasm_bindgen]
pub fn decrypt_ecies(encrypted_msg: Vec<u8>, private_key_bytes: Vec<u8>) -> Result<Vec<u8>, Error> {
    match spark_frost::bridge::decrypt_ecies(encrypted_msg, private_key_bytes) {
        Ok(v) => Ok(v),
        Err(e) => Err(Error::Spark(e)),
    }
}

#[wasm_bindgen]
pub fn get_taproot_pubkey(verifying_pubkey: Vec<u8>) -> Result<Vec<u8>, Error> {
    let full_key = bitcoin::PublicKey::from_slice(&verifying_pubkey)
        .map_err(|e| Error::Spark(e.to_string()))?;
    let x_only_key = full_key.inner.x_only_public_key().0;

    let secp = Secp256k1::new();
    let (tweaked_pubkey, parity) = x_only_key.tap_tweak(&secp, None);

    let mut buf = [0u8; 33];
    buf[0] = match parity {
        Parity::Even => 0x02,
        Parity::Odd => 0x03,
    };
    buf[1..].clone_from_slice(&tweaked_pubkey.serialize());

    Ok(buf.to_vec())
}

#[wasm_bindgen]
pub fn get_public_key_bytes(
    private_key_bytes: Vec<u8>,
    compressed: bool,
) -> Result<Vec<u8>, Error> {
    let secp = Secp256k1::new();
    let secret_key = SecretKey::from_slice(&private_key_bytes)
        .map_err(|e| format!("Invalid private key: {}", e))?; // String auto-converts to Error
    let public_key = PublicKey::from_secret_key(&secp, &secret_key);

    Ok(if compressed {
        public_key.serialize().to_vec()
    } else {
        public_key.serialize_uncompressed().to_vec()
    })
}

#[wasm_bindgen]
pub fn verify_signature_bytes(
    signature: Vec<u8>,
    message: Vec<u8>,
    public_key: Vec<u8>,
) -> Result<bool, Error> {
    let secp = Secp256k1::new();

    let sig = if signature.len() == 64 {
        Signature::from_compact(&signature)
            .map_err(|e| Error::Spark(format!("Invalid compact signature: {}", e)))?
    } else {
        Signature::from_der(&signature)
            .map_err(|e| Error::Spark(format!("Invalid DER signature: {}", e)))?
    };

    let msg = Message::from_digest_slice(&message)
        .map_err(|e| Error::Spark(format!("Invalid message: {}", e)))?;
    let pubkey = PublicKey::from_slice(&public_key)
        .map_err(|e| Error::Spark(format!("Invalid pubkey: {}", e)))?;

    Ok(secp.verify_ecdsa(&msg, &sig, &pubkey).is_ok())
}

#[wasm_bindgen]
pub fn random_secret_key_bytes() -> Result<Vec<u8>, Error> {
    let secret_key: SecretKey = SecretKey::new(&mut rand::thread_rng());
    Ok(secret_key.secret_bytes().to_vec())
}

// ---------------------------------------------------------------------------
// Timelock functions — thin wrappers around spark_frost::transaction
// ---------------------------------------------------------------------------

#[wasm_bindgen]
pub fn get_timelock_from_sequence(sequence: u32) -> u32 {
    spark_frost::transaction::get_timelock_from_sequence(sequence)
}

#[wasm_bindgen]
pub fn check_if_valid_sequence(sequence: u32) -> Result<(), Error> {
    spark_frost::transaction::check_if_valid_sequence(sequence).map_err(Error::Spark)
}

#[wasm_bindgen]
pub fn is_zero_timelock(sequence: u32) -> bool {
    spark_frost::transaction::is_zero_timelock(sequence)
}

#[wasm_bindgen]
pub fn round_down_to_timelock_interval(timelock: u32, time_lock_interval: u32) -> u32 {
    spark_frost::transaction::round_down_to_timelock_interval(timelock, time_lock_interval)
}

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct TimelockResult {
    #[wasm_bindgen]
    pub next_sequence: u32,
    #[wasm_bindgen]
    pub next_direct_sequence: u32,
}

#[wasm_bindgen]
pub fn next_sequence(
    curr_sequence: u32,
    time_lock_interval: u32,
    direct_timelock_offset: u32,
) -> Result<TimelockResult, Error> {
    let (next_seq, next_direct_seq) = spark_frost::transaction::next_sequence(
        curr_sequence,
        time_lock_interval,
        direct_timelock_offset,
    )
    .map_err(Error::Spark)?;
    Ok(TimelockResult {
        next_sequence: next_seq,
        next_direct_sequence: next_direct_seq,
    })
}

// ---------------------------------------------------------------------------
// Node tx pair and refund tx trio
// ---------------------------------------------------------------------------

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct NodeTxPairResult {
    #[wasm_bindgen(getter_with_clone)]
    pub cpfp: TransactionResult,
    #[wasm_bindgen(getter_with_clone)]
    pub direct: TransactionResult,
}

fn internal_to_tx_result(
    r: spark_frost::transaction::InternalTransactionResult,
) -> Result<TransactionResult, Error> {
    let tx: Transaction = deserialize(&r.tx_bytes)
        .map_err(|e| Error::Spark(format!("failed to deserialize tx: {e}")))?;
    Ok(TransactionResult {
        tx: r.tx_bytes,
        sighash: r.sighash,
        inputs: tx.input.into_iter().map(|i| i.into()).collect(),
    })
}

#[wasm_bindgen]
pub fn construct_node_tx_pair(
    parent_tx: Vec<u8>,
    vout: u32,
    address: String,
    sequence: u32,
    direct_sequence: u32,
    fee_sats: u64,
) -> Result<NodeTxPairResult, Error> {
    let result = spark_frost::transaction::construct_node_tx_pair(
        &parent_tx,
        vout,
        &address,
        sequence,
        direct_sequence,
        fee_sats,
    )
    .map_err(Error::Spark)?;

    Ok(NodeTxPairResult {
        cpfp: internal_to_tx_result(result.cpfp)?,
        direct: internal_to_tx_result(result.direct)?,
    })
}

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct RefundTxTrioResult {
    #[wasm_bindgen(getter_with_clone)]
    pub cpfp_refund: TransactionResult,
    #[wasm_bindgen(getter_with_clone)]
    pub direct_refund: Option<TransactionResult>,
    #[wasm_bindgen(getter_with_clone)]
    pub direct_from_cpfp_refund: TransactionResult,
}

#[wasm_bindgen]
#[allow(clippy::too_many_arguments)]
pub fn construct_refund_tx_trio(
    cpfp_node_tx: Vec<u8>,
    direct_node_tx: Option<Vec<u8>>,
    vout: u32,
    receiving_pubkey: Vec<u8>,
    network: String,
    sequence: u32,
    direct_sequence: u32,
    fee_sats: u64,
) -> Result<RefundTxTrioResult, Error> {
    let direct_ref = direct_node_tx.as_deref();
    let result = spark_frost::transaction::construct_refund_tx_trio(
        &cpfp_node_tx,
        direct_ref,
        vout,
        &receiving_pubkey,
        &network,
        sequence,
        direct_sequence,
        fee_sats,
    )
    .map_err(Error::Spark)?;

    Ok(RefundTxTrioResult {
        cpfp_refund: internal_to_tx_result(result.cpfp_refund)?,
        direct_refund: result
            .direct_refund
            .map(internal_to_tx_result)
            .transpose()?,
        direct_from_cpfp_refund: internal_to_tx_result(result.direct_from_cpfp_refund)?,
    })
}

// ---------------------------------------------------------------------------
// Multi-input sighash
// ---------------------------------------------------------------------------

#[wasm_bindgen]
pub fn compute_multi_input_sighash(
    tx: Vec<u8>,
    input_index: u32,
    prev_out_scripts: JsValue,
    prev_out_values: JsValue,
) -> Result<Vec<u8>, Error> {
    let scripts: Vec<Vec<u8>> = serde_wasm_bindgen::from_value(prev_out_scripts)
        .map_err(|e| Error::Spark(format!("failed to deserialize prev_out_scripts: {e}")))?;
    let values: Vec<u64> = serde_wasm_bindgen::from_value(prev_out_values)
        .map_err(|e| Error::Spark(format!("failed to deserialize prev_out_values: {e}")))?;
    spark_frost::transaction::compute_multi_input_sighash(&tx, input_index, &scripts, &values)
        .map_err(Error::Spark)
}

pub fn compute_multi_input_sighash_uniffi(
    tx: Vec<u8>,
    input_index: u32,
    prev_out_scripts: Vec<Vec<u8>>,
    prev_out_values: Vec<u64>,
) -> Result<Vec<u8>, Error> {
    spark_frost::transaction::compute_multi_input_sighash(
        &tx,
        input_index,
        &prev_out_scripts,
        &prev_out_values,
    )
    .map_err(Error::Spark)
}

// ---------------------------------------------------------------------------
// HTLC functions
// ---------------------------------------------------------------------------

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct HTLCSpendResult {
    #[wasm_bindgen(getter_with_clone)]
    pub tx: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub sighash: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub script: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub control_block: Vec<u8>,
}

#[wasm_bindgen]
#[allow(clippy::too_many_arguments)]
pub fn construct_htlc_transaction(
    node_tx: Vec<u8>,
    vout: u32,
    sequence: u32,
    payment_hash: Vec<u8>,
    hashlock_pubkey: Vec<u8>,
    seqlock_pubkey: Vec<u8>,
    htlc_sequence: u32,
    apply_fee: bool,
    fee_sats: u64,
    network: String,
) -> Result<TransactionResult, Error> {
    let hash: [u8; 32] = payment_hash
        .try_into()
        .map_err(|_| Error::Spark("payment_hash must be 32 bytes".to_string()))?;
    let result = spark_frost::htlc::construct_htlc_transaction(
        &node_tx,
        vout,
        sequence,
        &hash,
        &hashlock_pubkey,
        &seqlock_pubkey,
        htlc_sequence,
        apply_fee,
        fee_sats,
        &network,
    )
    .map_err(Error::Spark)?;
    internal_to_tx_result(result)
}

#[wasm_bindgen]
#[allow(clippy::too_many_arguments)]
pub fn construct_htlc_sender_spend(
    htlc_tx: Vec<u8>,
    destination_pubkey: Vec<u8>,
    payment_hash: Vec<u8>,
    hashlock_pubkey: Vec<u8>,
    seqlock_pubkey: Vec<u8>,
    htlc_sequence: u32,
    fee_sats: u64,
    network: String,
) -> Result<HTLCSpendResult, Error> {
    let hash: [u8; 32] = payment_hash
        .try_into()
        .map_err(|_| Error::Spark("payment_hash must be 32 bytes".to_string()))?;
    let result = spark_frost::htlc::construct_htlc_sender_spend(
        &htlc_tx,
        &destination_pubkey,
        &hash,
        &hashlock_pubkey,
        &seqlock_pubkey,
        htlc_sequence,
        fee_sats,
        &network,
    )
    .map_err(Error::Spark)?;
    Ok(HTLCSpendResult {
        tx: result.tx_bytes,
        sighash: result.sighash,
        script: result.script,
        control_block: result.control_block,
    })
}

#[wasm_bindgen]
#[allow(clippy::too_many_arguments)]
pub fn construct_htlc_receiver_spend(
    htlc_tx: Vec<u8>,
    destination_pubkey: Vec<u8>,
    payment_hash: Vec<u8>,
    hashlock_pubkey: Vec<u8>,
    seqlock_pubkey: Vec<u8>,
    htlc_sequence: u32,
    fee_sats: u64,
    network: String,
) -> Result<HTLCSpendResult, Error> {
    let hash: [u8; 32] = payment_hash
        .try_into()
        .map_err(|_| Error::Spark("payment_hash must be 32 bytes".to_string()))?;
    let result = spark_frost::htlc::construct_htlc_receiver_spend(
        &htlc_tx,
        &destination_pubkey,
        &hash,
        &hashlock_pubkey,
        &seqlock_pubkey,
        htlc_sequence,
        fee_sats,
        &network,
    )
    .map_err(Error::Spark)?;
    Ok(HTLCSpendResult {
        tx: result.tx_bytes,
        sighash: result.sighash,
        script: result.script,
        control_block: result.control_block,
    })
}

// ---------------------------------------------------------------------------
// Adaptor signature functions
// ---------------------------------------------------------------------------

#[wasm_bindgen]
#[derive(Debug, Clone)]
pub struct AdaptorSignatureResult {
    #[wasm_bindgen(getter_with_clone)]
    pub signature: Vec<u8>,
    #[wasm_bindgen(getter_with_clone)]
    pub adaptor_private_key: Vec<u8>,
}

#[wasm_bindgen]
pub fn generate_adaptor_from_signature(
    signature: Vec<u8>,
) -> Result<AdaptorSignatureResult, Error> {
    let (adaptor_sig, adaptor_key) =
        spark_frost::adaptor_signature::generate_adaptor_from_signature(&signature)
            .map_err(Error::Spark)?;
    Ok(AdaptorSignatureResult {
        signature: adaptor_sig,
        adaptor_private_key: adaptor_key,
    })
}

#[wasm_bindgen]
pub fn generate_signature_from_existing_adaptor(
    signature: Vec<u8>,
    adaptor_private_key: Vec<u8>,
) -> Result<Vec<u8>, Error> {
    spark_frost::adaptor_signature::generate_signature_from_existing_adaptor(
        &signature,
        &adaptor_private_key,
    )
    .map_err(Error::Spark)
}

#[wasm_bindgen]
pub fn validate_adaptor_signature(
    pub_key: Vec<u8>,
    hash: Vec<u8>,
    signature: Vec<u8>,
    adaptor_pub_key: Vec<u8>,
) -> Result<(), Error> {
    spark_frost::adaptor_signature::validate_adaptor_signature(
        &pub_key,
        &hash,
        &signature,
        &adaptor_pub_key,
    )
    .map_err(Error::Spark)
}

#[wasm_bindgen]
pub fn apply_adaptor_to_signature(
    pub_key: Vec<u8>,
    hash: Vec<u8>,
    signature: Vec<u8>,
    adaptor_private_key: Vec<u8>,
) -> Result<Vec<u8>, Error> {
    spark_frost::adaptor_signature::apply_adaptor_to_signature(
        &pub_key,
        &hash,
        &signature,
        &adaptor_private_key,
    )
    .map_err(Error::Spark)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_get_taproot_pubkey() {
        let pubkey_bytes =
            hex::decode("031cd7599775b6959193029794b04dcd99d257cbec008d63e49fdf0f89a5f7c231")
                .expect("Invalid hex");

        let taproot_pubkey =
            get_taproot_pubkey(pubkey_bytes).expect("should compute taproot pubkey");

        // It should be 33 bytes: 1-byte prefix + 32-byte x-only key
        assert_eq!(taproot_pubkey.len(), 33);

        let expected_taproot_bytes =
            hex::decode("02133ca1b125d35d19a8c794b0b04b8968e24091fd6685247c1b0e903c6f5bdd23")
                .expect("Invalid hex");
        assert_eq!(taproot_pubkey, expected_taproot_bytes);
    }
}
