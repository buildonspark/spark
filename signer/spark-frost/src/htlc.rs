use bitcoin::{
    absolute::LockTime,
    consensus::serialize,
    hashes::Hash,
    key::Secp256k1,
    secp256k1::XOnlyPublicKey,
    sighash::{Prevouts, SighashCache},
    taproot::{LeafVersion, TaprootBuilder, TaprootSpendInfo},
    transaction::Version,
    Address, Amount, OutPoint, ScriptBuf, Sequence, TapSighashType, Transaction, TxIn, TxOut,
    Witness,
};

use crate::transaction::{
    check_if_valid_sequence, deser_tx, ephemeral_anchor_output, maybe_apply_fee,
    p2tr_script_from_pubkey_bytes, parse_network, InternalTransactionResult,
};

/// NUMS (Nothing Up My Sleeve) point for taproot internal key.
/// From BIP-341: hash of the generator point G on secp256k1.
/// Matches Go's `NUMSPoint` constant.
const NUMS_POINT_HEX: &str = "50929b74c1a04954b78b4b6035e97a5e078a5a0f28ec96d547bfee9ace803ac0";

fn nums_x_only_pubkey() -> XOnlyPublicKey {
    let bytes = hex::decode(NUMS_POINT_HEX).expect("valid NUMS hex");
    XOnlyPublicKey::from_slice(&bytes).expect("valid NUMS x-only key")
}

/// Parse a 33-byte compressed pubkey into an x-only public key.
fn parse_x_only_pubkey(pubkey_bytes: &[u8]) -> Result<XOnlyPublicKey, String> {
    let full_key =
        bitcoin::PublicKey::from_slice(pubkey_bytes).map_err(|e| format!("invalid pubkey: {e}"))?;
    Ok(full_key.inner.x_only_public_key().0)
}

/// Create a hash lock script: OP_SHA256 <hash> OP_EQUALVERIFY <x-only-pubkey> OP_CHECKSIG
/// Matches Go's `CreateHashLockScript`.
///
/// Go test vector:
///   hash = "02d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1"
///   pk   = "0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83"
///   =>     "a82002d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1882047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac"
pub fn create_hash_lock_script(hash: &[u8; 32], pubkey: &XOnlyPublicKey) -> ScriptBuf {
    let pk_bytes = pubkey.serialize();
    bitcoin::script::Builder::new()
        .push_opcode(bitcoin::opcodes::all::OP_SHA256)
        .push_slice(hash)
        .push_opcode(bitcoin::opcodes::all::OP_EQUALVERIFY)
        .push_slice(pk_bytes)
        .push_opcode(bitcoin::opcodes::all::OP_CHECKSIG)
        .into_script()
}

/// Create a sequence lock script: <sequence> OP_CSV OP_DROP <x-only-pubkey> OP_CHECKSIG
/// Matches Go's `CreateSequenceLockScript`.
///
/// Uses `push_int` for minimal integer encoding:
/// - 0 => OP_0
/// - 1-16 => OP_1..OP_16
/// - >16 => minimal CScriptNum bytes
///
/// Go test vectors:
///   sequence=0:    "00b275..."  (OP_0)
///   sequence=15:   "5fb275..."  (OP_15 = 0x5f)
///   sequence=2160: "027008b275..." (push 2 bytes: 0x70 0x08)
pub fn create_sequence_lock_script(sequence: u32, pubkey: &XOnlyPublicKey) -> ScriptBuf {
    let pk_bytes = pubkey.serialize();
    bitcoin::script::Builder::new()
        .push_int(sequence as i64)
        .push_opcode(bitcoin::opcodes::all::OP_CSV)
        .push_opcode(bitcoin::opcodes::all::OP_DROP)
        .push_slice(pk_bytes)
        .push_opcode(bitcoin::opcodes::all::OP_CHECKSIG)
        .into_script()
}

/// Build a TaprootSpendInfo with two leaves (hash lock and sequence lock).
/// Internal key is the NUMS point (no known private key).
/// Matches Go's taproot tree construction via `AssembleTaprootScriptTree(hashLockLeaf, sequenceLockLeaf)`.
fn build_htlc_taproot_spend_info(
    hash: &[u8; 32],
    hashlock_pubkey: &XOnlyPublicKey,
    htlc_sequence: u32,
    seqlock_pubkey: &XOnlyPublicKey,
) -> Result<TaprootSpendInfo, String> {
    let hash_lock_script = create_hash_lock_script(hash, hashlock_pubkey);
    let seq_lock_script = create_sequence_lock_script(htlc_sequence, seqlock_pubkey);

    // Go's AssembleTaprootScriptTree places leaves at depth 1 in order:
    // leaf 0 = hashLockLeaf, leaf 1 = sequenceLockLeaf
    let builder = TaprootBuilder::new()
        .add_leaf(1, hash_lock_script)
        .map_err(|e| format!("failed to add hash lock leaf: {e}"))?
        .add_leaf(1, seq_lock_script)
        .map_err(|e| format!("failed to add seq lock leaf: {e}"))?;

    let nums = nums_x_only_pubkey();
    let secp = Secp256k1::verification_only();

    builder
        .finalize(&secp, nums)
        .map_err(|e| format!("failed to finalize taproot: {e:?}"))
}

/// Result type for HTLC spend transactions.
pub struct HTLCSpendResult {
    pub tx_bytes: Vec<u8>,
    pub sighash: Vec<u8>,
    pub script: Vec<u8>,
    pub control_block: Vec<u8>,
}

/// Construct an HTLC transaction.
/// The output is P2TR with the HTLC taproot script tree.
/// The input is signed via key-spend (by the node's aggregate key).
/// Matches Go's `CreateLightningHTLCTransactionWithSequence`.
#[allow(clippy::too_many_arguments)]
pub fn construct_htlc_transaction(
    node_tx_bytes: &[u8],
    vout: u32,
    sequence: u32,
    payment_hash: &[u8; 32],
    hashlock_pubkey: &[u8],
    seqlock_pubkey: &[u8],
    htlc_sequence: u32,
    apply_fee: bool,
    fee_sats: u64,
    network: &str,
) -> Result<InternalTransactionResult, String> {
    check_if_valid_sequence(sequence)?;
    check_if_valid_sequence(htlc_sequence)?;
    let node_tx = deser_tx(node_tx_bytes)?;
    let net = parse_network(network)?;

    if vout as usize >= node_tx.output.len() {
        return Err("invalid vout index".to_string());
    }

    let prev_output = &node_tx.output[vout as usize];
    let amount = prev_output.value.to_sat();

    let output_amount = if apply_fee {
        maybe_apply_fee(amount, fee_sats)
    } else {
        amount
    };

    let hashlock_xonly = parse_x_only_pubkey(hashlock_pubkey)?;
    let seqlock_xonly = parse_x_only_pubkey(seqlock_pubkey)?;

    let spend_info = build_htlc_taproot_spend_info(
        payment_hash,
        &hashlock_xonly,
        htlc_sequence,
        &seqlock_xonly,
    )?;

    // Build the P2TR output address from the taproot spend info
    let addr = Address::p2tr_tweaked(spend_info.output_key(), net);
    let output_script = addr.script_pubkey();

    let outpoint = OutPoint::new(node_tx.compute_txid(), vout);
    let input = TxIn {
        previous_output: outpoint,
        script_sig: ScriptBuf::new(),
        sequence: Sequence::from_consensus(sequence),
        witness: Witness::new(),
    };

    let mut outputs = vec![TxOut {
        value: Amount::from_sat(output_amount),
        script_pubkey: output_script,
    }];

    if !apply_fee {
        outputs.push(ephemeral_anchor_output());
    }

    let new_tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input],
        output: outputs,
    };

    let sighash = SighashCache::new(&new_tx)
        .taproot_key_spend_signature_hash(
            0,
            &Prevouts::All(&[prev_output]),
            TapSighashType::Default,
        )
        .map_err(|e| format!("sighash error: {e}"))?;

    Ok(InternalTransactionResult {
        tx_bytes: serialize(&new_tx),
        sighash: sighash.as_raw_hash().to_byte_array().to_vec(),
    })
}

/// Construct an HTLC sender spend transaction (sequence lock path).
/// The sender can reclaim funds after the CSV timelock expires.
/// Matches Go's `createSenderSpendTx` test helper and the sequence lock leaf path.
#[allow(clippy::too_many_arguments)]
pub fn construct_htlc_sender_spend(
    htlc_tx_bytes: &[u8],
    destination_pubkey: &[u8],
    payment_hash: &[u8; 32],
    hashlock_pubkey: &[u8],
    seqlock_pubkey: &[u8],
    htlc_sequence: u32,
    fee_sats: u64,
    network: &str,
) -> Result<HTLCSpendResult, String> {
    check_if_valid_sequence(htlc_sequence)?;
    let htlc_tx = deser_tx(htlc_tx_bytes)?;
    let net = parse_network(network)?;

    if htlc_tx.output.is_empty() {
        return Err("HTLC tx has no outputs".to_string());
    }
    let prev_output = &htlc_tx.output[0];
    let amount = prev_output.value.to_sat();
    let output_amount = maybe_apply_fee(amount, fee_sats);

    let dest_script = p2tr_script_from_pubkey_bytes(destination_pubkey, net)?;

    let hashlock_xonly = parse_x_only_pubkey(hashlock_pubkey)?;
    let seqlock_xonly = parse_x_only_pubkey(seqlock_pubkey)?;

    let spend_info = build_htlc_taproot_spend_info(
        payment_hash,
        &hashlock_xonly,
        htlc_sequence,
        &seqlock_xonly,
    )?;

    let seq_lock_script = create_sequence_lock_script(htlc_sequence, &seqlock_xonly);

    let outpoint = OutPoint::new(htlc_tx.compute_txid(), 0);
    let input = TxIn {
        previous_output: outpoint,
        script_sig: ScriptBuf::new(),
        sequence: Sequence::from_consensus(htlc_sequence), // Must match CSV
        witness: Witness::new(),
    };

    let spend_tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input],
        output: vec![TxOut {
            value: Amount::from_sat(output_amount),
            script_pubkey: dest_script,
        }],
    };

    // Get the control block for the sequence lock leaf
    let leaf_version = LeafVersion::TapScript;
    let control_block = spend_info
        .control_block(&(seq_lock_script.clone(), leaf_version))
        .ok_or("failed to get control block for sequence lock leaf")?;

    // Compute BIP-342 script-path sighash
    let sighash = SighashCache::new(&spend_tx)
        .taproot_script_spend_signature_hash(
            0,
            &Prevouts::All(&[prev_output]),
            bitcoin::taproot::TapLeafHash::from_script(&seq_lock_script, leaf_version),
            TapSighashType::Default,
        )
        .map_err(|e| format!("script-path sighash error: {e}"))?;

    Ok(HTLCSpendResult {
        tx_bytes: serialize(&spend_tx),
        sighash: sighash.as_raw_hash().to_byte_array().to_vec(),
        script: seq_lock_script.to_bytes(),
        control_block: control_block.serialize(),
    })
}

/// Construct an HTLC receiver spend transaction (hash lock path).
/// The receiver can claim funds by providing the preimage.
/// Matches Go's `createReceiverSpendTx` test helper and the hash lock leaf path.
#[allow(clippy::too_many_arguments)]
pub fn construct_htlc_receiver_spend(
    htlc_tx_bytes: &[u8],
    destination_pubkey: &[u8],
    payment_hash: &[u8; 32],
    hashlock_pubkey: &[u8],
    seqlock_pubkey: &[u8],
    htlc_sequence: u32,
    fee_sats: u64,
    network: &str,
) -> Result<HTLCSpendResult, String> {
    check_if_valid_sequence(htlc_sequence)?;
    let htlc_tx = deser_tx(htlc_tx_bytes)?;
    let net = parse_network(network)?;

    if htlc_tx.output.is_empty() {
        return Err("HTLC tx has no outputs".to_string());
    }
    let prev_output = &htlc_tx.output[0];
    let amount = prev_output.value.to_sat();
    let output_amount = maybe_apply_fee(amount, fee_sats);

    let dest_script = p2tr_script_from_pubkey_bytes(destination_pubkey, net)?;

    let hashlock_xonly = parse_x_only_pubkey(hashlock_pubkey)?;
    let seqlock_xonly = parse_x_only_pubkey(seqlock_pubkey)?;

    let spend_info = build_htlc_taproot_spend_info(
        payment_hash,
        &hashlock_xonly,
        htlc_sequence,
        &seqlock_xonly,
    )?;

    let hash_lock_script = create_hash_lock_script(payment_hash, &hashlock_xonly);

    let outpoint = OutPoint::new(htlc_tx.compute_txid(), 0);
    let input = TxIn {
        previous_output: outpoint,
        script_sig: ScriptBuf::new(),
        sequence: Sequence::MAX, // No CSV lock for hash path
        witness: Witness::new(),
    };

    let spend_tx = Transaction {
        version: Version::non_standard(3),
        lock_time: LockTime::ZERO,
        input: vec![input],
        output: vec![TxOut {
            value: Amount::from_sat(output_amount),
            script_pubkey: dest_script,
        }],
    };

    // Get the control block for the hash lock leaf
    let leaf_version = LeafVersion::TapScript;
    let control_block = spend_info
        .control_block(&(hash_lock_script.clone(), leaf_version))
        .ok_or("failed to get control block for hash lock leaf")?;

    // Compute BIP-342 script-path sighash
    let sighash = SighashCache::new(&spend_tx)
        .taproot_script_spend_signature_hash(
            0,
            &Prevouts::All(&[prev_output]),
            bitcoin::taproot::TapLeafHash::from_script(&hash_lock_script, leaf_version),
            TapSighashType::Default,
        )
        .map_err(|e| format!("script-path sighash error: {e}"))?;

    Ok(HTLCSpendResult {
        tx_bytes: serialize(&spend_tx),
        sighash: sighash.as_raw_hash().to_byte_array().to_vec(),
        script: hash_lock_script.to_bytes(),
        control_block: control_block.serialize(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    // ---------------------------------------------------------------------------
    // Script tests — matching Go test vectors byte-for-byte
    // ---------------------------------------------------------------------------

    #[test]
    fn test_create_hash_lock_script() {
        // Matches Go's TestCreateHashLockScript
        let hash = hex::decode("02d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1")
            .unwrap();
        let hash_arr: [u8; 32] = hash.try_into().unwrap();

        let pk_bytes =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let xonly = parse_x_only_pubkey(&pk_bytes).unwrap();

        let script = create_hash_lock_script(&hash_arr, &xonly);
        let expected = "a82002d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1882047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac";
        assert_eq!(hex::encode(script.as_bytes()), expected);
    }

    #[test]
    fn test_create_sequence_lock_script() {
        // Matches Go's TestCreateSequenceLockScript
        let pk_bytes =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let xonly = parse_x_only_pubkey(&pk_bytes).unwrap();

        let cases = vec![
            // (name, sequence, expected_hex)
            (
                "0 sequence",
                0u32,
                "00b2752047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac",
            ),
            (
                "< 16 sequence",
                15u32,
                "5fb2752047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac",
            ),
            (
                "> 16 sequence",
                2160u32,
                "027008b2752047997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83ac",
            ),
        ];

        for (name, sequence, expected) in cases {
            let script = create_sequence_lock_script(sequence, &xonly);
            assert_eq!(
                hex::encode(script.as_bytes()),
                expected,
                "failed for {name}"
            );
        }
    }

    // ---------------------------------------------------------------------------
    // Taproot address test — matching Go's TestCreateHTLCTaprootAddress
    // ---------------------------------------------------------------------------

    #[test]
    fn test_create_htlc_taproot_address() {
        // Matches Go's TestCreateHTLCTaprootAddress
        let hash = hex::decode("02d3bb7a73d1cbdf5193f69bfdac92143703b4e90d7e993dd5644bdda1c0bde1")
            .unwrap();
        let hash_arr: [u8; 32] = hash.try_into().unwrap();

        let pk1_bytes =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let pk1 = parse_x_only_pubkey(&pk1_bytes).unwrap();

        let pk2_bytes =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();
        let pk2 = parse_x_only_pubkey(&pk2_bytes).unwrap();

        let spend_info = build_htlc_taproot_spend_info(&hash_arr, &pk1, 2160, &pk2).unwrap();
        let addr = Address::p2tr_tweaked(spend_info.output_key(), bitcoin::Network::Regtest);

        assert_eq!(
            addr.to_string(),
            "bcrt1p0kdvjnm6mz6zzhnkxhhdw6gemt9cjyvmnn48evlfx7s9hn3a8dxqq7g3eg"
        );
    }

    // ---------------------------------------------------------------------------
    // HTLC transaction test — matching Go's TestCreateLightningHTLCTransaction_BuildsExpectedTxFromExpectedParams
    // ---------------------------------------------------------------------------

    #[test]
    fn test_construct_htlc_transaction_matches_go() {
        // Matches Go's TestCreateLightningHTLCTransaction_BuildsExpectedTxFromExpectedParams

        // Parse the node tx (rawTx from Go test)
        let raw_tx_hex = "0300000000010180d6e3ba8082893627a42f2770fdb2e900731638258a2d04cd6b8b2f7a982e150000000000d0070040020002000000000000225120d04e30f634945d8b59283c10831cfab354d6d9cb88d1f7adfdba67cb8a7734f500000000000000000451024e730140ebcc474fdc71b83fe5f547976e418e91025ef8b323b572f68e709b82c36c7303496ee315c3b3b710af59c14f8d2aa97b9a0bc40b778385b32c59f7e0f34fabb200000000";
        let raw_tx_bytes = hex::decode(raw_tx_hex).unwrap();

        // Parse the refund tx to derive the sequence
        let raw_refund_tx_hex = "03000000000101d4b9193b8a28d4a986a15f17f5fe4e310c1d73e34865a24d04d39e37dddaccff00000000006c0700000200020000000000002251200686f6870264df6673c066f0591d38b5d60636f4f7a58143b88cbdff327cb68000000000000000000451024e73014003bb8cccc5b494ac9eb2b510618e5c54bd0082c5c5ba0838c9411f3d432dd4a0ec59ec4b4274006a2761040d8aa54702bc01dfed165035c2beaa017e5acc79c100000000";
        let raw_refund_tx_bytes = hex::decode(raw_refund_tx_hex).unwrap();
        let refund_tx: Transaction = bitcoin::consensus::deserialize(&raw_refund_tx_bytes).unwrap();
        let refund_sequence = refund_tx.input[0].sequence.to_consensus_u32();
        let sequence = refund_sequence - 30; // Go: sequence := rawRefundTx.TxIn[0].Sequence - 30

        let hash_hex = "10d31aeabd2bf7cdcba3a229107a4edb7b1c5b35c90c2fca491bd127c68069bd";
        let hash_bytes = hex::decode(hash_hex).unwrap();
        let hash_arr: [u8; 32] = hash_bytes.try_into().unwrap();

        let hashlock_pk =
            hex::decode("028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4")
                .unwrap();
        let seqlock_pk =
            hex::decode("032f0db1a8b99ad42e75e2f1cf4d977511a6d94587b4482c77fbd1fe9acc456a27")
                .unwrap();

        let result = construct_htlc_transaction(
            &raw_tx_bytes,
            0,
            sequence,
            &hash_arr,
            &hashlock_pk,
            &seqlock_pk,
            2160, // LightningHTLCSequence
            false,
            955,
            "regtest",
        )
        .unwrap();

        // Go expected tx (with witness stripped for comparison)
        let expected_with_witness_hex = "03000000000101d4b9193b8a28d4a986a15f17f5fe4e310c1d73e34865a24d04d39e37dddaccff00000000004e0700000200020000000000002251207898ca6a523e1724e99e3f6eb9bbd36eba16e6b15304921854e3c6b1174574b200000000000000000451024e7301406edf601068e37dc1222de88f2cbceaf9bcaa391683a7f393a40a68dc37d8765a7fae02793c4c3981101d6f35a9b9cd3a901c5f109f58a76ffaf8838c80670b5900000000";
        let expected_with_witness_bytes = hex::decode(expected_with_witness_hex).unwrap();
        let expected_tx: Transaction =
            bitcoin::consensus::deserialize(&expected_with_witness_bytes).unwrap();

        // Compare no-witness serialization (Go test compares SerializeTxNoWitness)
        let result_tx: Transaction = bitcoin::consensus::deserialize(&result.tx_bytes).unwrap();

        // Compare key fields
        assert_eq!(result_tx.version, expected_tx.version, "version mismatch");
        assert_eq!(
            result_tx.lock_time, expected_tx.lock_time,
            "locktime mismatch"
        );
        assert_eq!(
            result_tx.input.len(),
            expected_tx.input.len(),
            "input count mismatch"
        );
        assert_eq!(
            result_tx.input[0].previous_output, expected_tx.input[0].previous_output,
            "prev outpoint mismatch"
        );
        assert_eq!(
            result_tx.input[0].sequence, expected_tx.input[0].sequence,
            "sequence mismatch"
        );
        assert_eq!(
            result_tx.output.len(),
            expected_tx.output.len(),
            "output count mismatch"
        );
        assert_eq!(
            result_tx.output[0].value, expected_tx.output[0].value,
            "output 0 value mismatch"
        );
        assert_eq!(
            result_tx.output[0].script_pubkey, expected_tx.output[0].script_pubkey,
            "output 0 script mismatch — taproot address does not match Go"
        );
        // Second output is ephemeral anchor
        assert_eq!(
            result_tx.output[1].value, expected_tx.output[1].value,
            "anchor value mismatch"
        );
        assert_eq!(
            result_tx.output[1].script_pubkey, expected_tx.output[1].script_pubkey,
            "anchor script mismatch"
        );
    }

    #[test]
    fn test_construct_htlc_transaction_basic() {
        // Simpler test matching Go's TestCreateLightningHTLCTransaction_BuildsExpectedTx pattern
        let node_tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(100_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };
        let node_tx_bytes = serialize(&node_tx);

        let hash = [0x11u8; 32];
        // Use known test pubkeys
        let hashlock_pk =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let seqlock_pk =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();

        let sequence = 12345u32;

        let result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            sequence,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            2160, // LightningHTLCSequence
            false,
            955,
            "regtest",
        )
        .unwrap();

        let htlc_tx: Transaction = bitcoin::consensus::deserialize(&result.tx_bytes).unwrap();

        // 1 input, 2 outputs (HTLC + ephemeral anchor)
        assert_eq!(htlc_tx.input.len(), 1);
        assert_eq!(htlc_tx.output.len(), 2);
        assert_eq!(htlc_tx.input[0].sequence.to_consensus_u32(), sequence);
        assert_eq!(htlc_tx.output[0].value.to_sat(), 100_000); // No fee
        assert_eq!(htlc_tx.output[1].value.to_sat(), 0); // Anchor
        assert_eq!(result.sighash.len(), 32);
    }

    #[test]
    fn test_construct_htlc_direct_subtracts_fee() {
        // Matches Go's TestCreateDirectLightningHTLCTransaction_SubtractsFee
        let node_tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(50_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };
        let node_tx_bytes = serialize(&node_tx);

        let hash = [0x22u8; 32];
        let hashlock_pk =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let seqlock_pk =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();

        let result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            54321,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            2160,
            true, // apply fee
            955,
            "regtest",
        )
        .unwrap();

        let htlc_tx: Transaction = bitcoin::consensus::deserialize(&result.tx_bytes).unwrap();
        assert_eq!(htlc_tx.output.len(), 1); // No anchor when fee applied
        assert_eq!(htlc_tx.output[0].value.to_sat(), 50_000 - 955);
    }

    #[test]
    fn test_construct_htlc_sender_spend() {
        // Create an HTLC tx, then spend via sender (sequence lock) path
        let hash = [0x11u8; 32];
        let hashlock_pk =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let seqlock_pk =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();

        let node_tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(100_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };
        let node_tx_bytes = serialize(&node_tx);

        let htlc_result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            0,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            5, // small sequence for test
            true,
            955,
            "regtest",
        )
        .unwrap();

        let spend_result = construct_htlc_sender_spend(
            &htlc_result.tx_bytes,
            &seqlock_pk,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            5,
            955,
            "regtest",
        )
        .unwrap();

        let spend_tx: Transaction =
            bitcoin::consensus::deserialize(&spend_result.tx_bytes).unwrap();
        assert_eq!(spend_tx.input.len(), 1);
        assert_eq!(spend_tx.output.len(), 1);
        assert_eq!(spend_tx.input[0].sequence.to_consensus_u32(), 5); // CSV match
        assert_eq!(spend_result.sighash.len(), 32);
        assert!(!spend_result.script.is_empty());
        assert!(!spend_result.control_block.is_empty());
    }

    #[test]
    fn test_construct_htlc_receiver_spend() {
        // Create an HTLC tx, then spend via receiver (hash lock) path
        let hash = [0x11u8; 32];
        let hashlock_pk =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let seqlock_pk =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();

        let node_tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(100_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };
        let node_tx_bytes = serialize(&node_tx);

        let htlc_result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            0,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            5,
            true,
            955,
            "regtest",
        )
        .unwrap();

        let spend_result = construct_htlc_receiver_spend(
            &htlc_result.tx_bytes,
            &hashlock_pk,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            5,
            955,
            "regtest",
        )
        .unwrap();

        let spend_tx: Transaction =
            bitcoin::consensus::deserialize(&spend_result.tx_bytes).unwrap();
        assert_eq!(spend_tx.input.len(), 1);
        assert_eq!(spend_tx.output.len(), 1);
        assert_eq!(spend_tx.input[0].sequence.to_consensus_u32(), 0xFFFFFFFF); // Sequence::MAX
        assert_eq!(spend_result.sighash.len(), 32);
        assert!(!spend_result.script.is_empty());
        assert!(!spend_result.control_block.is_empty());
    }

    #[test]
    fn test_construct_htlc_transaction_rejects_invalid_sequence() {
        let node_tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(100_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };
        let node_tx_bytes = serialize(&node_tx);
        let hash = [0x11u8; 32];
        let hashlock_pk =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let seqlock_pk =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();

        // Bit 31 on sequence
        let result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            1 << 31 | 100,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            2160,
            false,
            955,
            "regtest",
        );
        assert!(result.as_ref().err().unwrap().contains("bit 31"));

        // Bit 22 on htlc_sequence
        let result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            100,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            1 << 22 | 2160,
            false,
            955,
            "regtest",
        );
        assert!(result.as_ref().err().unwrap().contains("bit 22"));
    }

    #[test]
    fn test_construct_htlc_sender_spend_rejects_invalid_sequence() {
        // Build a valid HTLC tx first
        let node_tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(100_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };
        let node_tx_bytes = serialize(&node_tx);
        let hash = [0x11u8; 32];
        let hashlock_pk =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let seqlock_pk =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();

        let htlc_result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            0,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            5,
            true,
            955,
            "regtest",
        )
        .unwrap();

        // Bit 31 on htlc_sequence
        let result = construct_htlc_sender_spend(
            &htlc_result.tx_bytes,
            &seqlock_pk,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            1 << 31 | 5,
            955,
            "regtest",
        );
        assert!(result.as_ref().err().unwrap().contains("bit 31"));
    }

    #[test]
    fn test_construct_htlc_receiver_spend_rejects_invalid_sequence() {
        let node_tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(100_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };
        let node_tx_bytes = serialize(&node_tx);
        let hash = [0x11u8; 32];
        let hashlock_pk =
            hex::decode("0247997a5c32ccf934257a675c306bf6ec37019358240156628af62baad7066a83")
                .unwrap();
        let seqlock_pk =
            hex::decode("03b66b574670a7b6bea89c0548903f70a6f059fd9abe737dc4c5aafe14a127408f")
                .unwrap();

        let htlc_result = construct_htlc_transaction(
            &node_tx_bytes,
            0,
            0,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            5,
            true,
            955,
            "regtest",
        )
        .unwrap();

        // Bit 22 on htlc_sequence
        let result = construct_htlc_receiver_spend(
            &htlc_result.tx_bytes,
            &hashlock_pk,
            &hash,
            &hashlock_pk,
            &seqlock_pk,
            1 << 22 | 5,
            955,
            "regtest",
        );
        assert!(result.as_ref().err().unwrap().contains("bit 22"));
    }
}
