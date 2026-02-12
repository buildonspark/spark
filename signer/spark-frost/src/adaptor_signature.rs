use bitcoin::hashes::{sha256, Hash, HashEngine};
use bitcoin::key::Parity;
use bitcoin::secp256k1::{self, PublicKey, Scalar, Secp256k1, SecretKey, XOnlyPublicKey};
use rand::Rng;

/// Generates an adaptor signature and a random adaptor private key from a valid Schnorr signature.
///
/// The adaptor signature has `s' = s - t` where `t` is the adaptor secret.
/// Returns `(adaptor_signature_bytes, adaptor_private_key_bytes)`.
pub fn generate_adaptor_from_signature(signature: &[u8]) -> Result<(Vec<u8>, Vec<u8>), String> {
    let (r_bytes, s_bytes) = parse_signature(signature)?;

    let adaptor_secret = random_secret_key()?;
    let adaptor_bytes = adaptor_secret.secret_bytes();

    let s_key = SecretKey::from_slice(&s_bytes).map_err(|e| format!("invalid s value: {e}"))?;
    let neg_adaptor_scalar: Scalar = adaptor_secret.negate().into();
    let new_s = s_key
        .add_tweak(&neg_adaptor_scalar)
        .map_err(|e| format!("scalar addition failed: {e}"))?;

    let mut new_sig = [0u8; 64];
    new_sig[..32].copy_from_slice(&r_bytes);
    new_sig[32..].copy_from_slice(&new_s.secret_bytes());

    Ok((new_sig.to_vec(), adaptor_bytes.to_vec()))
}

/// Generates an adaptor signature from a valid Schnorr signature using an existing adaptor private key.
///
/// Same math as `generate_adaptor_from_signature` but with a caller-supplied key.
pub fn generate_signature_from_existing_adaptor(
    signature: &[u8],
    adaptor_private_key: &[u8],
) -> Result<Vec<u8>, String> {
    let (r_bytes, s_bytes) = parse_signature(signature)?;

    let adaptor_secret = SecretKey::from_slice(adaptor_private_key)
        .map_err(|e| format!("invalid adaptor private key: {e}"))?;

    let s_key = SecretKey::from_slice(&s_bytes).map_err(|e| format!("invalid s value: {e}"))?;
    let neg_adaptor_scalar: Scalar = adaptor_secret.negate().into();
    let new_s = s_key
        .add_tweak(&neg_adaptor_scalar)
        .map_err(|e| format!("scalar addition failed: {e}"))?;

    let mut new_sig = [0u8; 64];
    new_sig[..32].copy_from_slice(&r_bytes);
    new_sig[32..].copy_from_slice(&new_s.secret_bytes());

    Ok(new_sig.to_vec())
}

/// Validates an adaptor signature against the original signer's public key and adaptor public key.
///
/// Tries both the adaptor public key and its negation (to handle parity).
pub fn validate_adaptor_signature(
    pub_key: &[u8],
    hash: &[u8],
    signature: &[u8],
    adaptor_pub_key: &[u8],
) -> Result<(), String> {
    let sig = parse_signature_internal(signature)?;
    let public_key = parse_public_key(pub_key)?;
    let adaptor_public = parse_public_key(adaptor_pub_key)?;

    if schnorr_verify_with_adaptor(&sig, hash, &public_key, &adaptor_public).is_ok() {
        return Ok(());
    }

    // Try with negated adaptor pubkey
    let secp = Secp256k1::new();
    let neg_adaptor = adaptor_public.negate(&secp);
    schnorr_verify_with_adaptor(&sig, hash, &public_key, &neg_adaptor)
}

/// Applies an adaptor private key to an adaptor signature to recover a valid Schnorr signature.
///
/// Tries `s + t` first, then `s - t`, verifying each candidate with standard BIP-340.
pub fn apply_adaptor_to_signature(
    pub_key: &[u8],
    hash: &[u8],
    signature: &[u8],
    adaptor_private_key: &[u8],
) -> Result<Vec<u8>, String> {
    let (r_bytes, s_bytes) = parse_signature(signature)?;

    let s_key = SecretKey::from_slice(&s_bytes).map_err(|e| format!("invalid s value: {e}"))?;
    let adaptor = SecretKey::from_slice(adaptor_private_key)
        .map_err(|e| format!("invalid adaptor private key: {e}"))?;

    // Try s + t
    if let Some(sig) = try_apply_tweak(pub_key, hash, &r_bytes, &s_key, &adaptor, false) {
        return Ok(sig);
    }

    // Try s - t
    if let Some(sig) = try_apply_tweak(pub_key, hash, &r_bytes, &s_key, &adaptor, true) {
        return Ok(sig);
    }

    Err("cannot apply adaptor to signature".to_string())
}

fn try_apply_tweak(
    pub_key: &[u8],
    hash: &[u8],
    r_bytes: &[u8; 32],
    s_key: &SecretKey,
    adaptor: &SecretKey,
    negate: bool,
) -> Option<Vec<u8>> {
    let tweak: Scalar = if negate {
        adaptor.negate().into()
    } else {
        (*adaptor).into()
    };

    let new_s = match s_key.add_tweak(&tweak) {
        Ok(k) => k,
        Err(_) => return None,
    };

    let mut sig_bytes = [0u8; 64];
    sig_bytes[..32].copy_from_slice(r_bytes);
    sig_bytes[32..].copy_from_slice(&new_s.secret_bytes());

    if verify_schnorr(pub_key, hash, &sig_bytes).is_ok() {
        Some(sig_bytes.to_vec())
    } else {
        None
    }
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

fn random_secret_key() -> Result<SecretKey, String> {
    let mut rng = rand::thread_rng();
    loop {
        let mut bytes = [0u8; 32];
        rng.fill(&mut bytes);
        if let Ok(sk) = SecretKey::from_slice(&bytes) {
            return Ok(sk);
        }
    }
}

struct SchnorrSig {
    r: [u8; 32],
    s: [u8; 32],
}

fn parse_signature(sig: &[u8]) -> Result<([u8; 32], [u8; 32]), String> {
    if sig.len() < 64 {
        return Err(format!(
            "malformed signature: too short: {} < 64",
            sig.len()
        ));
    }
    if sig.len() > 64 {
        return Err(format!("malformed signature: too long: {} > 64", sig.len()));
    }
    let mut r = [0u8; 32];
    let mut s = [0u8; 32];
    r.copy_from_slice(&sig[..32]);
    s.copy_from_slice(&sig[32..]);
    Ok((r, s))
}

fn parse_signature_internal(sig: &[u8]) -> Result<SchnorrSig, String> {
    let (r, s) = parse_signature(sig)?;
    Ok(SchnorrSig { r, s })
}

fn parse_public_key(key_bytes: &[u8]) -> Result<PublicKey, String> {
    match key_bytes.len() {
        33 | 65 => PublicKey::from_slice(key_bytes).map_err(|e| format!("invalid public key: {e}")),
        32 => {
            let xonly = XOnlyPublicKey::from_slice(key_bytes)
                .map_err(|e| format!("invalid x-only public key: {e}"))?;
            Ok(PublicKey::from_x_only_public_key(xonly, Parity::Even))
        }
        _ => Err(format!("invalid public key length: {}", key_bytes.len())),
    }
}

/// BIP-340 tagged hash: `SHA256(SHA256(tag) || SHA256(tag) || data)`.
fn bip340_challenge_hash(r: &[u8; 32], p: &[u8; 32], msg: &[u8; 32]) -> [u8; 32] {
    let tag_hash = sha256::Hash::hash(b"BIP0340/challenge");
    let mut engine = sha256::Hash::engine();
    engine.input(tag_hash.as_ref());
    engine.input(tag_hash.as_ref());
    engine.input(r);
    engine.input(p);
    engine.input(msg);
    sha256::Hash::from_engine(engine).to_byte_array()
}

/// Modified BIP-340 verification that adds `adaptor_pub_key` to the computed R point.
fn schnorr_verify_with_adaptor(
    sig: &SchnorrSig,
    hash: &[u8],
    public_key: &PublicKey,
    adaptor_pub_key: &PublicKey,
) -> Result<(), String> {
    let secp = Secp256k1::new();

    // Step 1: message must be 32 bytes
    if hash.len() != 32 {
        return Err(format!(
            "wrong size for message (got {}, want 32)",
            hash.len()
        ));
    }
    let hash_arr: &[u8; 32] = hash.try_into().unwrap();

    // Step 2: lift_x(P) — always use even-Y representation
    let (xonly_pk, _) = public_key.x_only_public_key();
    let pk_bytes = xonly_pk.serialize();

    // Step 5: e = tagged_hash("BIP0340/challenge", R || P || m) mod n
    let commitment = bip340_challenge_hash(&sig.r, &pk_bytes, hash_arr);
    let e_key = SecretKey::from_slice(&commitment)
        .map_err(|_| "hash of (R || P || m) too big".to_string())?;
    let neg_e_scalar: Scalar = e_key.negate().into();

    // Step 6: R = s*G - e*P
    let s_key =
        SecretKey::from_slice(&sig.s).map_err(|_| "invalid s value in signature".to_string())?;
    let s_g = PublicKey::from_secret_key(&secp, &s_key);

    let lifted_pk = PublicKey::from_x_only_public_key(xonly_pk, Parity::Even);
    let neg_e_p = lifted_pk
        .mul_tweak(&secp, &neg_e_scalar)
        .map_err(|e| format!("point multiplication failed: {e}"))?;

    let r_point = PublicKey::combine_keys(&[&s_g, &neg_e_p])
        .map_err(|e| format!("point addition failed: {e}"))?;

    // Step 6.5: newR = R + adaptorPubKey
    let new_r = PublicKey::combine_keys(&[&r_point, adaptor_pub_key])
        .map_err(|e| format!("adaptor point addition failed: {e}"))?;

    // Step 8: Fail if newR.y is odd
    let (new_r_xonly, parity) = new_r.x_only_public_key();
    if parity != Parity::Even {
        return Err("calculated R y-value is odd".to_string());
    }

    // Step 9: Verify newR.x == R
    let r_xonly =
        XOnlyPublicKey::from_slice(&sig.r).map_err(|e| format!("invalid r value: {e}"))?;
    if new_r_xonly != r_xonly {
        return Err("calculated R point was not given R".to_string());
    }

    Ok(())
}

/// Standard BIP-340 Schnorr verification.
fn verify_schnorr(pub_key_bytes: &[u8], hash: &[u8], sig_bytes: &[u8]) -> Result<(), String> {
    let secp = Secp256k1::new();
    let public_key = parse_public_key(pub_key_bytes)?;
    let (xonly, _) = public_key.x_only_public_key();
    let sig = secp256k1::schnorr::Signature::from_slice(sig_bytes)
        .map_err(|e| format!("invalid signature: {e}"))?;
    let msg =
        secp256k1::Message::from_digest_slice(hash).map_err(|e| format!("invalid message: {e}"))?;
    secp.verify_schnorr(&sig, &msg, &xonly)
        .map_err(|e| format!("verification failed: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use bitcoin::hashes::sha256;
    use bitcoin::secp256k1::Keypair;

    fn random_keypair() -> Keypair {
        let secp = Secp256k1::new();
        let sk = random_secret_key().unwrap();
        Keypair::from_secret_key(&secp, &sk)
    }

    /// Helper: generate a keypair, sign a message, return (pubkey_bytes, hash, sig_bytes).
    fn schnorr_sign(msg: &[u8]) -> (Vec<u8>, [u8; 32], Vec<u8>) {
        let secp = Secp256k1::new();
        let keypair = random_keypair();
        let pub_key = keypair.public_key();
        let hash = sha256::Hash::hash(msg).to_byte_array();
        let msg = secp256k1::Message::from_digest_slice(&hash).unwrap();
        let sig = secp.sign_schnorr_no_aux_rand(&msg, &keypair);
        (pub_key.serialize().to_vec(), hash, sig[..].to_vec())
    }

    #[test]
    fn test_adaptor_round_trip_1000() {
        for _ in 0..1000 {
            let (pub_key, hash, sig_bytes) = schnorr_sign(b"test");

            // Verify the original signature is valid
            assert!(verify_schnorr(&pub_key, &hash, &sig_bytes).is_ok());

            // Generate adaptor
            let (adaptor_sig, adaptor_priv) = generate_adaptor_from_signature(&sig_bytes).unwrap();

            // Derive adaptor public key
            let secp = Secp256k1::new();
            let adaptor_secret = SecretKey::from_slice(&adaptor_priv).unwrap();
            let adaptor_pub = PublicKey::from_secret_key(&secp, &adaptor_secret);

            // Validate adaptor signature
            validate_adaptor_signature(&pub_key, &hash, &adaptor_sig, &adaptor_pub.serialize())
                .unwrap();

            // Apply adaptor to recover valid signature
            let recovered =
                apply_adaptor_to_signature(&pub_key, &hash, &adaptor_sig, &adaptor_priv).unwrap();

            // Recovered signature must be valid
            assert!(verify_schnorr(&pub_key, &hash, &recovered).is_ok());
        }
    }

    #[test]
    fn test_validate_valid_signature() {
        let (pub_key, hash, sig_bytes) = schnorr_sign(b"test message for adaptor signature");

        let (adaptor_sig, adaptor_priv) = generate_adaptor_from_signature(&sig_bytes).unwrap();

        let secp = Secp256k1::new();
        let adaptor_pub =
            PublicKey::from_secret_key(&secp, &SecretKey::from_slice(&adaptor_priv).unwrap());

        validate_adaptor_signature(&pub_key, &hash, &adaptor_sig, &adaptor_pub.serialize())
            .unwrap();
    }

    #[test]
    fn test_validate_invalid_signature_bytes() {
        let (pub_key, hash, _) = schnorr_sign(b"test");
        let secp = Secp256k1::new();
        let adaptor_pub = PublicKey::from_secret_key(&secp, &random_secret_key().unwrap());
        let adaptor_pub_bytes = adaptor_pub.serialize();

        // Too short
        let err = validate_adaptor_signature(&pub_key, &hash, &[0u8; 32], &adaptor_pub_bytes)
            .unwrap_err();
        assert!(err.contains("malformed signature: too short"));

        // Too long
        let err = validate_adaptor_signature(&pub_key, &hash, &[0u8; 80], &adaptor_pub_bytes)
            .unwrap_err();
        assert!(err.contains("malformed signature: too long"));

        // Empty
        let err = validate_adaptor_signature(&pub_key, &hash, &[], &adaptor_pub_bytes).unwrap_err();
        assert!(err.contains("malformed signature: too short"));

        // Invalid r (all 0xFF), s = 0x00 — should error (s=0 is invalid SecretKey)
        let mut bad_sig = [0u8; 64];
        for b in bad_sig[..32].iter_mut() {
            *b = 0xFF;
        }
        let err =
            validate_adaptor_signature(&pub_key, &hash, &bad_sig, &adaptor_pub_bytes).unwrap_err();
        assert!(!err.is_empty());
    }

    #[test]
    fn test_validate_invalid_hash() {
        let (pub_key, hash, sig_bytes) = schnorr_sign(b"test");
        let (adaptor_sig, adaptor_priv) = generate_adaptor_from_signature(&sig_bytes).unwrap();

        let secp = Secp256k1::new();
        let adaptor_pub =
            PublicKey::from_secret_key(&secp, &SecretKey::from_slice(&adaptor_priv).unwrap());
        let adaptor_pub_bytes = adaptor_pub.serialize();

        // Wrong-sized hashes
        for len in [0, 1, 16, 31, 33, 64, 128] {
            let bad_hash = vec![0u8; len];
            let err =
                validate_adaptor_signature(&pub_key, &bad_hash, &adaptor_sig, &adaptor_pub_bytes)
                    .unwrap_err();
            assert!(
                err.contains("wrong size for message"),
                "expected 'wrong size' error for len={len}, got: {err}"
            );
        }

        // Correct length hash, but wrong value
        let _ = hash; // suppress unused warning
    }

    #[test]
    fn test_validate_signature_mismatch() {
        let (pub_key, hash, sig_bytes) = schnorr_sign(b"test message");
        let (adaptor_sig, _) = generate_adaptor_from_signature(&sig_bytes).unwrap();

        // Wrong adaptor public key
        let secp = Secp256k1::new();
        let wrong_pub = PublicKey::from_secret_key(&secp, &random_secret_key().unwrap());
        assert!(
            validate_adaptor_signature(&pub_key, &hash, &adaptor_sig, &wrong_pub.serialize(),)
                .is_err()
        );

        // Different message, different adaptor — wrong hash should fail
        let (_, diff_hash, diff_sig) = schnorr_sign(b"different message");
        let (diff_adaptor_sig, diff_adaptor_priv) =
            generate_adaptor_from_signature(&diff_sig).unwrap();
        let diff_adaptor_pub =
            PublicKey::from_secret_key(&secp, &SecretKey::from_slice(&diff_adaptor_priv).unwrap());
        assert!(validate_adaptor_signature(
            &pub_key,
            &hash, // original hash, not diff_hash
            &diff_adaptor_sig,
            &diff_adaptor_pub.serialize(),
        )
        .is_err());
        let _ = diff_hash;
    }

    #[test]
    fn test_validate_wrong_public_key() {
        let (_, hash, sig_bytes) = schnorr_sign(b"test");
        let (adaptor_sig, adaptor_priv) = generate_adaptor_from_signature(&sig_bytes).unwrap();

        let secp = Secp256k1::new();
        let adaptor_pub =
            PublicKey::from_secret_key(&secp, &SecretKey::from_slice(&adaptor_priv).unwrap());

        // Sign with one key but validate against a different key
        let wrong_keypair = random_keypair();
        let wrong_pub = wrong_keypair.public_key().serialize();

        assert!(validate_adaptor_signature(
            &wrong_pub,
            &hash,
            &adaptor_sig,
            &adaptor_pub.serialize(),
        )
        .is_err());
    }

    #[test]
    fn test_validate_repeated() {
        let (pub_key, hash, sig_bytes) = schnorr_sign(b"test");
        let (adaptor_sig, adaptor_priv) = generate_adaptor_from_signature(&sig_bytes).unwrap();

        let secp = Secp256k1::new();
        let adaptor_pub =
            PublicKey::from_secret_key(&secp, &SecretKey::from_slice(&adaptor_priv).unwrap());
        let adaptor_pub_bytes = adaptor_pub.serialize();

        // Repeated validation should succeed every time
        for _ in 0..10 {
            validate_adaptor_signature(&pub_key, &hash, &adaptor_sig, &adaptor_pub_bytes).unwrap();
        }
    }

    #[test]
    fn test_generate_from_existing_adaptor() {
        let (pub_key, hash, sig_bytes) = schnorr_sign(b"test existing adaptor");

        let secp = Secp256k1::new();
        let adaptor_secret = random_secret_key().unwrap();
        let adaptor_pub = PublicKey::from_secret_key(&secp, &adaptor_secret);

        let adaptor_sig =
            generate_signature_from_existing_adaptor(&sig_bytes, &adaptor_secret.secret_bytes())
                .unwrap();

        // Validate
        validate_adaptor_signature(&pub_key, &hash, &adaptor_sig, &adaptor_pub.serialize())
            .unwrap();

        // Apply and verify
        let recovered = apply_adaptor_to_signature(
            &pub_key,
            &hash,
            &adaptor_sig,
            &adaptor_secret.secret_bytes(),
        )
        .unwrap();
        assert!(verify_schnorr(&pub_key, &hash, &recovered).is_ok());
    }

    #[test]
    fn test_apply_adaptor_fails_with_wrong_key() {
        let (pub_key, hash, sig_bytes) = schnorr_sign(b"test wrong key");
        let (adaptor_sig, _) = generate_adaptor_from_signature(&sig_bytes).unwrap();

        let wrong_key = random_secret_key().unwrap();
        let result =
            apply_adaptor_to_signature(&pub_key, &hash, &adaptor_sig, &wrong_key.secret_bytes());
        assert!(result.is_err());
    }
}
