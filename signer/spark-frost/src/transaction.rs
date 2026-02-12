use bitcoin::{
    absolute::LockTime,
    consensus::{deserialize, serialize},
    hashes::Hash,
    key::Secp256k1,
    sighash::{Prevouts, SighashCache},
    transaction::Version,
    Address, Amount, OutPoint, ScriptBuf, Sequence, TapSighashType, Transaction, TxIn, TxOut,
    Witness,
};
use std::str::FromStr;

// BIP68: bit 31 disables relative lock-time, bit 22 selects time-based lock
const SEQUENCE_LOCK_TIME_DISABLED: u32 = 1 << 31;
const SEQUENCE_LOCK_TIME_IS_SECONDS: u32 = 1 << 22;
// Lower 16 bits are the timelock value
const SEQUENCE_LOCK_TIME_MASK: u32 = 0x0000FFFF;

/// Ephemeral anchor output: OP_TRUE (0x51) + push 2 bytes (0x02) + 0x4e73, value 0.
/// Matches Go's `common.EphemeralAnchorOutput()`.
pub fn ephemeral_anchor_output() -> TxOut {
    TxOut {
        value: Amount::from_sat(0),
        script_pubkey: ScriptBuf::from_bytes(vec![0x51, 0x02, 0x4e, 0x73]),
    }
}

/// Parse network string to bitcoin::Network.
/// Matches Go's btcnetwork constants.
pub fn parse_network(s: &str) -> Result<bitcoin::Network, String> {
    match s {
        "mainnet" => Ok(bitcoin::Network::Bitcoin),
        "testnet" => Ok(bitcoin::Network::Testnet),
        "signet" => Ok(bitcoin::Network::Signet),
        "regtest" => Ok(bitcoin::Network::Regtest),
        _ => Err(format!("invalid network: {s}")),
    }
}

/// Build a P2TR script from a 33-byte compressed public key.
/// Matches Go's `common.P2TRScriptFromPubKey` which uses `ComputeTaprootKeyNoScript`.
pub fn p2tr_script_from_pubkey_bytes(
    pubkey: &[u8],
    network: bitcoin::Network,
) -> Result<ScriptBuf, String> {
    let full_key =
        bitcoin::PublicKey::from_slice(pubkey).map_err(|e| format!("invalid pubkey: {e}"))?;
    let x_only = full_key.inner.x_only_public_key().0;
    let secp = Secp256k1::new();
    let addr = Address::p2tr(&secp, x_only, None, network);
    Ok(addr.script_pubkey())
}

/// Subtract the default fee from an amount, but don't go below it.
/// Matches Go's `common.MaybeApplyFee`.
pub fn maybe_apply_fee(amount: u64, fee_sats: u64) -> u64 {
    if amount > fee_sats {
        amount - fee_sats
    } else {
        amount
    }
}

/// Deserialize raw bytes into a Bitcoin Transaction.
pub fn deser_tx(bytes: &[u8]) -> Result<Transaction, String> {
    deserialize(bytes).map_err(|e| format!("failed to deserialize tx: {e}"))
}

// ---------------------------------------------------------------------------
// Timelock functions — matching Go's bitcointransaction package
// ---------------------------------------------------------------------------

/// Extract the timelock (lower 16 bits) from a sequence number.
/// Matches Go's `GetTimelockFromSequence`.
pub fn get_timelock_from_sequence(sequence: u32) -> u32 {
    sequence & SEQUENCE_LOCK_TIME_MASK
}

/// Validate that bits 31 (disable) and 22 (time-based) are NOT set.
/// Matches Go's `GetAndValidateUserSequence` validation logic.
pub fn check_if_valid_sequence(sequence: u32) -> Result<(), String> {
    if sequence & SEQUENCE_LOCK_TIME_DISABLED != 0 {
        return Err("sequence has bit 31 set (timelock disabled)".to_string());
    }
    if sequence & SEQUENCE_LOCK_TIME_IS_SECONDS != 0 {
        return Err("sequence has bit 22 set (time-based timelock not supported)".to_string());
    }
    Ok(())
}

/// Check if the timelock in a sequence is zero.
/// Matches Go's `IsZeroNode` logic.
pub fn is_zero_timelock(sequence: u32) -> bool {
    get_timelock_from_sequence(sequence) == 0
}

/// Round down a timelock to the nearest multiple of time_lock_interval.
/// Matches Go's `RoundDownToTimelockInterval`.
pub fn round_down_to_timelock_interval(timelock: u32, time_lock_interval: u32) -> u32 {
    if time_lock_interval == 0 {
        return timelock;
    }
    timelock - (timelock % time_lock_interval)
}

/// Decrement timelock by one interval, preserving upper bits.
/// Returns (next_sequence, next_direct_sequence).
/// Matches Go's `bitcointransaction.NextSequence`.
pub fn next_sequence(
    curr_sequence: u32,
    time_lock_interval: u32,
    direct_timelock_offset: u32,
) -> Result<(u32, u32), String> {
    let curr_timelock = get_timelock_from_sequence(curr_sequence);
    let next_timelock = curr_timelock as i64 - time_lock_interval as i64;

    if next_timelock < 0 {
        return Err("next timelock interval is less than 0, call renew node timelock".to_string());
    }

    // Clear lower 16 bits, keep upper bits
    let upper_bits = curr_sequence & 0xFFFF0000;
    let next_seq = upper_bits | (next_timelock as u32);

    // Ensure direct_timelock_offset doesn't overflow into the upper 16 bits
    let next_direct_timelock = (next_timelock as u32) + direct_timelock_offset;
    if next_direct_timelock > 0xFFFF {
        return Err(format!(
            "direct timelock offset {direct_timelock_offset} overflows lower 16 bits (next_timelock={next_timelock})"
        ));
    }
    let next_direct_seq = upper_bits | next_direct_timelock;

    Ok((next_seq, next_direct_seq))
}

// ---------------------------------------------------------------------------
// Transaction construction helpers
// ---------------------------------------------------------------------------

/// Result of constructing a transaction: serialized tx + sighash.
pub struct InternalTransactionResult {
    pub tx_bytes: Vec<u8>,
    pub sighash: Vec<u8>,
}

/// Result of construct_node_tx_pair: CPFP and direct node transactions.
pub struct NodeTxPairResult {
    pub cpfp: InternalTransactionResult,
    pub direct: InternalTransactionResult,
}

/// Result of construct_refund_tx_trio: all three refund variants.
pub struct RefundTxTrioResult {
    pub cpfp_refund: InternalTransactionResult,
    pub direct_refund: Option<InternalTransactionResult>,
    pub direct_from_cpfp_refund: InternalTransactionResult,
}

/// Build a single-input, single-output-plus-optional-anchor transaction.
/// This is the shared builder used by node and refund tx construction.
fn build_single_output_tx(
    prev_tx: &Transaction,
    vout: u32,
    output_script: ScriptBuf,
    sequence: u32,
    apply_fee: bool,
    fee_sats: u64,
    include_anchor: bool,
) -> Result<InternalTransactionResult, String> {
    if vout as usize >= prev_tx.output.len() {
        return Err("invalid vout index".to_string());
    }

    let prev_output = &prev_tx.output[vout as usize];
    let prev_amount = prev_output.value.to_sat();

    let output_amount = if apply_fee {
        maybe_apply_fee(prev_amount, fee_sats)
    } else {
        prev_amount
    };

    let outpoint = OutPoint::new(prev_tx.compute_txid(), vout);
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

    if include_anchor {
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

/// Construct both CPFP and direct node transactions from a parent tx.
/// CPFP node tx: full amount, ephemeral anchor, uses `sequence`.
/// Direct node tx: fee deducted, no anchor, uses `direct_sequence`.
/// Matches Go's node tx construction patterns.
pub fn construct_node_tx_pair(
    parent_tx_bytes: &[u8],
    vout: u32,
    address: &str,
    sequence: u32,
    direct_sequence: u32,
    fee_sats: u64,
) -> Result<NodeTxPairResult, String> {
    check_if_valid_sequence(sequence)?;
    check_if_valid_sequence(direct_sequence)?;
    let prev_tx = deser_tx(parent_tx_bytes)?;

    let dest_address = Address::from_str(address)
        .map_err(|e| format!("invalid address: {e}"))?
        .assume_checked();
    let output_script = dest_address.script_pubkey();

    let cpfp = build_single_output_tx(
        &prev_tx,
        vout,
        output_script.clone(),
        sequence,
        false,
        fee_sats,
        true,
    )?;

    let direct = build_single_output_tx(
        &prev_tx,
        vout,
        output_script,
        direct_sequence,
        true,
        fee_sats,
        false,
    )?;

    Ok(NodeTxPairResult { cpfp, direct })
}

/// Construct all three refund transaction variants.
/// - cpfp_refund: spends cpfp node tx, no fee, with anchor
/// - direct_refund (optional): spends direct node tx, fee, no anchor
/// - direct_from_cpfp_refund: spends cpfp node tx, fee, no anchor
///
/// Matches Go's constructCPFPRefundTransaction, constructDirectRefundTransaction,
/// and constructDirectFromCPFPRefundTransaction.
#[allow(clippy::too_many_arguments)]
pub fn construct_refund_tx_trio(
    cpfp_node_tx_bytes: &[u8],
    direct_node_tx_bytes: Option<&[u8]>,
    vout: u32,
    receiving_pubkey: &[u8],
    network: &str,
    sequence: u32,
    direct_sequence: u32,
    fee_sats: u64,
) -> Result<RefundTxTrioResult, String> {
    check_if_valid_sequence(sequence)?;
    check_if_valid_sequence(direct_sequence)?;
    let net = parse_network(network)?;
    let output_script = p2tr_script_from_pubkey_bytes(receiving_pubkey, net)?;

    let cpfp_node_tx = deser_tx(cpfp_node_tx_bytes)?;

    // CPFP refund: no fee, with anchor
    let cpfp_refund = build_single_output_tx(
        &cpfp_node_tx,
        vout,
        output_script.clone(),
        sequence,
        false,
        fee_sats,
        true,
    )?;

    // Direct refund: fee, no anchor — only if direct node tx provided
    let direct_refund = if let Some(direct_bytes) = direct_node_tx_bytes {
        let direct_node_tx = deser_tx(direct_bytes)?;
        Some(build_single_output_tx(
            &direct_node_tx,
            vout,
            output_script.clone(),
            direct_sequence,
            true,
            fee_sats,
            false,
        )?)
    } else {
        None
    };

    // Direct-from-CPFP refund: spends cpfp node tx with direct_sequence, fee, no anchor
    let direct_from_cpfp_refund = build_single_output_tx(
        &cpfp_node_tx,
        vout,
        output_script,
        direct_sequence,
        true,
        fee_sats,
        false,
    )?;

    Ok(RefundTxTrioResult {
        cpfp_refund,
        direct_refund,
        direct_from_cpfp_refund,
    })
}

/// Compute BIP-341 sighash committing to all inputs' prevouts.
/// Matches Go's `SigHashFromMultiPrevOutTx` which uses a MultiPrevOutFetcher.
pub fn compute_multi_input_sighash(
    tx_bytes: &[u8],
    input_index: u32,
    prev_out_scripts: &[Vec<u8>],
    prev_out_values: &[u64],
) -> Result<Vec<u8>, String> {
    let tx = deser_tx(tx_bytes)?;

    if prev_out_scripts.len() != prev_out_values.len() {
        return Err("prev_out_scripts and prev_out_values must have same length".to_string());
    }
    if prev_out_scripts.len() != tx.input.len() {
        return Err("number of prev outputs must match number of inputs".to_string());
    }

    let prev_outputs: Vec<TxOut> = prev_out_scripts
        .iter()
        .zip(prev_out_values.iter())
        .map(|(script, &value)| TxOut {
            value: Amount::from_sat(value),
            script_pubkey: ScriptBuf::from_bytes(script.clone()),
        })
        .collect();

    let prev_refs: Vec<&TxOut> = prev_outputs.iter().collect();

    let sighash = SighashCache::new(&tx)
        .taproot_key_spend_signature_hash(
            input_index as usize,
            &Prevouts::All(&prev_refs),
            TapSighashType::Default,
        )
        .map_err(|e| format!("sighash error: {e}"))?;

    Ok(sighash.as_raw_hash().to_byte_array().to_vec())
}

#[cfg(test)]
mod tests {
    use super::*;
    use bitcoin::consensus::serialize as btc_serialize;

    // Go constants for tests: estimatedTxSize(191) * DefaultSatsPerVbyte(5) = 955
    const DEFAULT_FEE_SATS: u64 = 955;
    const TIME_LOCK_INTERVAL: u32 = 100;
    const DIRECT_TIMELOCK_OFFSET: u32 = 50;

    // ---------------------------------------------------------------------------
    // Timelock tests — matching Go's TestNextSequence, TestRoundDownToTimelockInterval
    // ---------------------------------------------------------------------------

    #[test]
    fn test_get_timelock_from_sequence() {
        assert_eq!(get_timelock_from_sequence(1000), 1000);
        assert_eq!(get_timelock_from_sequence(0xAAAA0500), 0x0500);
        assert_eq!(get_timelock_from_sequence(0), 0);
        assert_eq!(get_timelock_from_sequence(0xFFFF), 0xFFFF);
    }

    #[test]
    fn test_check_if_valid_sequence() {
        // Valid sequences
        assert!(check_if_valid_sequence(1000).is_ok());
        assert!(check_if_valid_sequence(1 << 30 | 1000).is_ok());

        // Bit 31 set
        assert!(check_if_valid_sequence(1 << 31 | 1000)
            .unwrap_err()
            .contains("bit 31"));

        // Bit 22 set
        assert!(check_if_valid_sequence(1 << 22 | 1000)
            .unwrap_err()
            .contains("bit 22"));
    }

    #[test]
    fn test_is_zero_timelock() {
        assert!(is_zero_timelock(0));
        assert!(is_zero_timelock(1 << 30)); // Upper bits don't matter
        assert!(!is_zero_timelock(1));
        assert!(!is_zero_timelock(1000));
    }

    #[test]
    fn test_round_down_to_timelock_interval() {
        // Matches Go's TestRoundDownToTimelockInterval
        assert_eq!(
            round_down_to_timelock_interval(100, TIME_LOCK_INTERVAL),
            100
        );
        assert_eq!(
            round_down_to_timelock_interval(1000, TIME_LOCK_INTERVAL),
            1000
        );
        assert_eq!(
            round_down_to_timelock_interval(740, TIME_LOCK_INTERVAL),
            700
        );
        assert_eq!(
            round_down_to_timelock_interval(670, TIME_LOCK_INTERVAL),
            600
        );
        assert_eq!(round_down_to_timelock_interval(0, TIME_LOCK_INTERVAL), 0);
        assert_eq!(
            round_down_to_timelock_interval(1970, TIME_LOCK_INTERVAL),
            1900
        );
        assert_eq!(
            round_down_to_timelock_interval(1870, TIME_LOCK_INTERVAL),
            1800
        );
    }

    #[test]
    fn test_next_sequence() {
        // Matches Go's TestNextSequence test cases exactly
        let cases = vec![
            // (name, curr_seq, want_seq, want_direct_seq)
            ("basic", 1000u32, 900u32, 950u32),
            (
                "mixed upper-word pattern",
                0xAAAA0500,
                0xAAAA049C,
                0xAAAA04CE,
            ),
            ("large timelock value", 65535, 65435, 65485),
            (
                "boundary at exactly one TimeLockInterval",
                100,
                0,
                DIRECT_TIMELOCK_OFFSET,
            ),
            (
                "multiple higher-order bits",
                1 << 30 | 1 << 29 | 1 << 16 | 2000,
                1 << 30 | 1 << 29 | 1 << 16 | 1900,
                1 << 30 | 1 << 29 | 1 << 16 | 1950,
            ),
            (
                "preserves higher-order bits",
                1 << 30 | 1000,
                1 << 30 | 900,
                1 << 30 | 950,
            ),
        ];

        for (name, curr_seq, want_seq, want_direct_seq) in cases {
            let (next_seq, next_direct_seq) =
                next_sequence(curr_seq, TIME_LOCK_INTERVAL, DIRECT_TIMELOCK_OFFSET)
                    .unwrap_or_else(|e| panic!("{name}: {e}"));
            assert_eq!(next_seq, want_seq, "{name}: next_seq mismatch");
            assert_eq!(
                next_direct_seq, want_direct_seq,
                "{name}: next_direct_seq mismatch"
            );

            // Verify upper bits preserved
            let input_upper = curr_seq & 0xFFFF0000;
            assert_eq!(
                next_seq & 0xFFFF0000,
                input_upper,
                "{name}: upper bits not preserved in next_seq"
            );
            assert_eq!(
                next_direct_seq & 0xFFFF0000,
                input_upper,
                "{name}: upper bits not preserved in next_direct_seq"
            );
        }
    }

    #[test]
    fn test_next_sequence_error_timelock_too_small() {
        // Matches Go's TestNextSequence_ErrorTimelockTooSmall
        let cases = vec![
            ("zero timelock", 0u32),
            ("less than interval", 99u32),
            ("less than interval with higher bits", 1u32 << 30 | 50),
        ];

        for (name, curr_seq) in cases {
            let result = next_sequence(curr_seq, TIME_LOCK_INTERVAL, DIRECT_TIMELOCK_OFFSET);
            assert!(result.is_err(), "{name}: expected error");
            assert!(
                result.unwrap_err().contains("less than 0"),
                "{name}: wrong error message"
            );
        }
    }

    #[test]
    fn test_next_sequence_error_direct_offset_overflow() {
        // If next_timelock + direct_timelock_offset > 0xFFFF, the upper bits would be corrupted.
        // Use a large offset that overflows the lower 16 bits.
        let curr_seq = 0xFFFF; // timelock = 65535, next = 65435
        let result = next_sequence(curr_seq, TIME_LOCK_INTERVAL, 0xFFFF);
        assert!(result.is_err(), "expected overflow error");
        assert!(
            result.unwrap_err().contains("overflows lower 16 bits"),
            "wrong error message"
        );
    }

    // ---------------------------------------------------------------------------
    // Transaction construction tests
    // ---------------------------------------------------------------------------

    fn make_dummy_prev_tx(amount_sats: u64) -> Transaction {
        Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![TxIn {
                previous_output: OutPoint::default(),
                script_sig: ScriptBuf::new(),
                sequence: Sequence::ZERO,
                witness: Witness::new(),
            }],
            output: vec![TxOut {
                value: Amount::from_sat(amount_sats),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]), // OP_TRUE placeholder
            }],
        }
    }

    #[test]
    fn test_ephemeral_anchor_output() {
        let anchor = ephemeral_anchor_output();
        assert_eq!(anchor.value, Amount::from_sat(0));
        assert_eq!(anchor.script_pubkey.as_bytes(), &[0x51, 0x02, 0x4e, 0x73]);
    }

    #[test]
    fn test_maybe_apply_fee() {
        assert_eq!(maybe_apply_fee(100_000, DEFAULT_FEE_SATS), 99_045);
        assert_eq!(maybe_apply_fee(955, DEFAULT_FEE_SATS), 955); // At boundary
        assert_eq!(maybe_apply_fee(500, DEFAULT_FEE_SATS), 500); // Below fee
        assert_eq!(maybe_apply_fee(956, DEFAULT_FEE_SATS), 1);
    }

    #[test]
    fn test_construct_node_tx_pair() {
        let prev_tx = make_dummy_prev_tx(100_000);
        let prev_tx_bytes = btc_serialize(&prev_tx);

        // Use a regtest P2TR address
        let secp = Secp256k1::new();
        let pubkey_bytes =
            hex::decode("031cd7599775b6959193029794b04dcd99d257cbec008d63e49fdf0f89a5f7c231")
                .unwrap();
        let full_key = bitcoin::PublicKey::from_slice(&pubkey_bytes).unwrap();
        let x_only = full_key.inner.x_only_public_key().0;
        let addr = Address::p2tr(&secp, x_only, None, bitcoin::Network::Regtest);
        let address = addr.to_string();

        let result = construct_node_tx_pair(
            &prev_tx_bytes,
            0,
            &address,
            1000, // CPFP sequence
            1050, // direct sequence
            DEFAULT_FEE_SATS,
        )
        .unwrap();

        // Verify CPFP: full amount, anchor output
        let cpfp_tx: Transaction = deserialize(&result.cpfp.tx_bytes).unwrap();
        assert_eq!(cpfp_tx.output.len(), 2);
        assert_eq!(cpfp_tx.output[0].value.to_sat(), 100_000);
        assert_eq!(cpfp_tx.output[1].value.to_sat(), 0); // anchor
        assert_eq!(cpfp_tx.input[0].sequence.to_consensus_u32(), 1000);

        // Verify direct: fee deducted, no anchor
        let direct_tx: Transaction = deserialize(&result.direct.tx_bytes).unwrap();
        assert_eq!(direct_tx.output.len(), 1);
        assert_eq!(
            direct_tx.output[0].value.to_sat(),
            100_000 - DEFAULT_FEE_SATS
        );
        assert_eq!(direct_tx.input[0].sequence.to_consensus_u32(), 1050);

        // Both should have non-empty sighashes
        assert_eq!(result.cpfp.sighash.len(), 32);
        assert_eq!(result.direct.sighash.len(), 32);
    }

    #[test]
    fn test_construct_refund_tx_trio() {
        let amount = 100_000u64;
        let pubkey_bytes =
            hex::decode("031cd7599775b6959193029794b04dcd99d257cbec008d63e49fdf0f89a5f7c231")
                .unwrap();

        // Create CPFP node tx and direct node tx
        let cpfp_node_tx = make_dummy_prev_tx(amount);
        let cpfp_bytes = btc_serialize(&cpfp_node_tx);

        let direct_node_tx = make_dummy_prev_tx(amount);
        let direct_bytes = btc_serialize(&direct_node_tx);

        let result = construct_refund_tx_trio(
            &cpfp_bytes,
            Some(&direct_bytes),
            0,
            &pubkey_bytes,
            "regtest",
            900, // CPFP refund sequence
            950, // direct sequence
            DEFAULT_FEE_SATS,
        )
        .unwrap();

        // CPFP refund: no fee, with anchor
        let cpfp_refund: Transaction = deserialize(&result.cpfp_refund.tx_bytes).unwrap();
        assert_eq!(cpfp_refund.output.len(), 2);
        assert_eq!(cpfp_refund.output[0].value.to_sat(), amount);
        assert_eq!(cpfp_refund.input[0].sequence.to_consensus_u32(), 900);

        // Direct refund: fee, no anchor
        let direct_refund: Transaction =
            deserialize(&result.direct_refund.as_ref().unwrap().tx_bytes).unwrap();
        assert_eq!(direct_refund.output.len(), 1);
        assert_eq!(
            direct_refund.output[0].value.to_sat(),
            amount - DEFAULT_FEE_SATS
        );
        assert_eq!(direct_refund.input[0].sequence.to_consensus_u32(), 950);

        // Direct-from-CPFP refund: fee, no anchor, spends CPFP node tx
        let dfcpfp: Transaction = deserialize(&result.direct_from_cpfp_refund.tx_bytes).unwrap();
        assert_eq!(dfcpfp.output.len(), 1);
        assert_eq!(dfcpfp.output[0].value.to_sat(), amount - DEFAULT_FEE_SATS);
        assert_eq!(dfcpfp.input[0].sequence.to_consensus_u32(), 950);

        // direct_from_cpfp spends the same prev tx as cpfp_refund
        assert_eq!(
            dfcpfp.input[0].previous_output.txid,
            cpfp_refund.input[0].previous_output.txid
        );
    }

    #[test]
    fn test_construct_refund_tx_trio_no_direct() {
        let amount = 100_000u64;
        let pubkey_bytes =
            hex::decode("031cd7599775b6959193029794b04dcd99d257cbec008d63e49fdf0f89a5f7c231")
                .unwrap();

        let cpfp_node_tx = make_dummy_prev_tx(amount);
        let cpfp_bytes = btc_serialize(&cpfp_node_tx);

        let result = construct_refund_tx_trio(
            &cpfp_bytes,
            None, // no direct node tx
            0,
            &pubkey_bytes,
            "regtest",
            900,
            950,
            DEFAULT_FEE_SATS,
        )
        .unwrap();

        assert!(result.direct_refund.is_none());
        assert!(!result.cpfp_refund.tx_bytes.is_empty());
        assert!(!result.direct_from_cpfp_refund.tx_bytes.is_empty());
    }

    #[test]
    fn test_construct_node_tx_pair_rejects_invalid_sequence() {
        let prev_tx = make_dummy_prev_tx(100_000);
        let prev_tx_bytes = btc_serialize(&prev_tx);
        let secp = Secp256k1::new();
        let pubkey_bytes =
            hex::decode("031cd7599775b6959193029794b04dcd99d257cbec008d63e49fdf0f89a5f7c231")
                .unwrap();
        let full_key = bitcoin::PublicKey::from_slice(&pubkey_bytes).unwrap();
        let x_only = full_key.inner.x_only_public_key().0;
        let addr = Address::p2tr(&secp, x_only, None, bitcoin::Network::Regtest);
        let address = addr.to_string();

        // Bit 31 on sequence
        let result = construct_node_tx_pair(
            &prev_tx_bytes,
            0,
            &address,
            1 << 31 | 1000,
            1050,
            DEFAULT_FEE_SATS,
        );
        assert!(result.as_ref().err().unwrap().contains("bit 31"));

        // Bit 22 on direct_sequence
        let result = construct_node_tx_pair(
            &prev_tx_bytes,
            0,
            &address,
            1000,
            1 << 22 | 1050,
            DEFAULT_FEE_SATS,
        );
        assert!(result.as_ref().err().unwrap().contains("bit 22"));
    }

    #[test]
    fn test_construct_refund_tx_trio_rejects_invalid_sequence() {
        let prev_tx = make_dummy_prev_tx(100_000);
        let prev_tx_bytes = btc_serialize(&prev_tx);
        let pubkey_bytes =
            hex::decode("031cd7599775b6959193029794b04dcd99d257cbec008d63e49fdf0f89a5f7c231")
                .unwrap();

        // Bit 31 on sequence
        let result = construct_refund_tx_trio(
            &prev_tx_bytes,
            None,
            0,
            &pubkey_bytes,
            "regtest",
            1 << 31 | 900,
            950,
            DEFAULT_FEE_SATS,
        );
        assert!(result.as_ref().err().unwrap().contains("bit 31"));

        // Bit 22 on direct_sequence
        let result = construct_refund_tx_trio(
            &prev_tx_bytes,
            None,
            0,
            &pubkey_bytes,
            "regtest",
            900,
            1 << 22 | 950,
            DEFAULT_FEE_SATS,
        );
        assert!(result.as_ref().err().unwrap().contains("bit 22"));
    }

    #[test]
    fn test_compute_multi_input_sighash() {
        // Create a tx with 2 inputs
        let prev_tx1 = make_dummy_prev_tx(50_000);
        let prev_tx2 = make_dummy_prev_tx(30_000);

        let tx = Transaction {
            version: Version::non_standard(3),
            lock_time: LockTime::ZERO,
            input: vec![
                TxIn {
                    previous_output: OutPoint::new(prev_tx1.compute_txid(), 0),
                    script_sig: ScriptBuf::new(),
                    sequence: Sequence::ZERO,
                    witness: Witness::new(),
                },
                TxIn {
                    previous_output: OutPoint::new(prev_tx2.compute_txid(), 0),
                    script_sig: ScriptBuf::new(),
                    sequence: Sequence::ZERO,
                    witness: Witness::new(),
                },
            ],
            output: vec![TxOut {
                value: Amount::from_sat(79_000),
                script_pubkey: ScriptBuf::from_bytes(vec![0x51]),
            }],
        };

        let tx_bytes = btc_serialize(&tx);
        let prev_scripts = vec![
            prev_tx1.output[0].script_pubkey.as_bytes().to_vec(),
            prev_tx2.output[0].script_pubkey.as_bytes().to_vec(),
        ];
        let prev_values = vec![50_000u64, 30_000u64];

        let sighash =
            compute_multi_input_sighash(&tx_bytes, 0, &prev_scripts, &prev_values).unwrap();
        assert_eq!(sighash.len(), 32);

        // Second input should produce different sighash
        let sighash2 =
            compute_multi_input_sighash(&tx_bytes, 1, &prev_scripts, &prev_values).unwrap();
        assert_eq!(sighash2.len(), 32);
        assert_ne!(sighash, sighash2);
    }
}
