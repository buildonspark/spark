use bitcoin::absolute::LockTime;
use bitcoin::hashes::Hash;
use bitcoin::transaction::Version;
use bitcoin::{
    Address, Amount, OutPoint, ScriptBuf, Sequence, Transaction, TxIn, TxOut, Txid, Witness,
};
use ecies::{decrypt, encrypt};
use std::io::Write;
use std::{collections::HashMap, fs::OpenOptions, str::FromStr};

use crate::{proto, signing};

#[derive(Clone, Debug)]
pub struct DummyTx {
    pub tx: Vec<u8>,
    pub txid: String,
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

pub fn create_dummy_tx(address: &str, amount_sats: u64) -> Result<DummyTx, String> {
    // Create a fake input that spends the all-zero txid.
    let input = TxIn {
        previous_output: OutPoint {
            txid: Txid::from_slice(&[0; 32]).map_err(|e| format!("txid err: {e}"))?,
            vout: 0,
        },
        script_sig: ScriptBuf::new(),
        sequence: Sequence::from_height(0),
        witness: Witness::new(),
    };

    let dest_address = Address::from_str(address)
        .map_err(|e| format!("invalid address: {e}"))?
        .assume_checked();

    let output = TxOut {
        value: Amount::from_sat(amount_sats),
        script_pubkey: dest_address.script_pubkey(),
    };

    let tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input],
        output: vec![output],
    };

    Ok(DummyTx {
        tx: bitcoin::consensus::serialize(&tx),
        txid: tx.compute_txid().to_string(),
    })
}

pub fn encrypt_ecies(msg: &[u8], public_key_bytes: &[u8]) -> Result<Vec<u8>, String> {
    encrypt(public_key_bytes, msg).map_err(|e| e.to_string())
}

pub fn decrypt_ecies(
    encrypted_msg: Vec<u8>,
    private_key_bytes: Vec<u8>,
) -> Result<Vec<u8>, String> {
    decrypt(&private_key_bytes, &encrypted_msg).map_err(|e| e.to_string())
}

pub fn sign_frost(
    msg: Vec<u8>,
    key_package: proto::frost::KeyPackage,
    nonce: proto::frost::SigningNonce,
    self_commitment: proto::common::SigningCommitment,
    statechain_commitments: HashMap<String, proto::common::SigningCommitment>,
    adaptor_public_key: Option<Vec<u8>>,
) -> Result<Vec<u8>, String> {
    log_to_file("Entering sign_frost");
    // Using a fixed UUID instead of generating a random one
    let job_id = "00000000-0000-0000-0000-000000000000".to_string();

    let signing_job = proto::frost::FrostSigningJob {
        job_id,
        message: msg,
        key_package: Some(key_package.clone()),
        nonce: Some(nonce),
        user_commitments: Some(self_commitment),
        verifying_key: key_package.public_key.clone(),
        commitments: statechain_commitments,
        adaptor_public_key: adaptor_public_key.unwrap_or_default(),
    };

    let request = proto::frost::SignFrostRequest {
        signing_jobs: vec![signing_job],
        role: proto::frost::SigningRole::User.into(),
    };

    let response = signing::sign_frost(&request).map_err(|e| e.to_string())?;
    let result = response
        .results
        .iter()
        .next()
        .ok_or_else(|| "No result".to_string())?
        .1
        .clone();

    Ok(result.signature_share)
}

#[allow(clippy::too_many_arguments)]
pub fn aggregate_frost(
    msg: Vec<u8>,
    statechain_commitments: HashMap<String, proto::common::SigningCommitment>,
    self_commitment: proto::common::SigningCommitment,
    statechain_signatures: HashMap<String, Vec<u8>>,
    self_signature: Vec<u8>,
    statechain_public_keys: HashMap<String, Vec<u8>>,
    self_public_key: Vec<u8>,
    verifying_key: Vec<u8>,
    adaptor_public_key: Option<Vec<u8>>,
) -> Result<Vec<u8>, String> {
    let request = proto::frost::AggregateFrostRequest {
        message: msg,
        commitments: statechain_commitments,
        user_commitments: Some(self_commitment),
        user_public_key: self_public_key,
        signature_shares: statechain_signatures,
        public_shares: statechain_public_keys,
        verifying_key,
        user_signature_share: self_signature,
        adaptor_public_key: adaptor_public_key.unwrap_or_default(),
    };

    let response = signing::aggregate_frost(&request).map_err(|e| e.to_string())?;

    Ok(response.signature)
}
