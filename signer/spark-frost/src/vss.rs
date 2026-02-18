#[cfg(test)]
use k256::elliptic_curve::Field;
use k256::{
    elliptic_curve::{group::GroupEncoding, sec1::ToEncodedPoint, PrimeField},
    AffinePoint, ProjectivePoint, Scalar,
};
use rand::rngs::OsRng;
use vsss_rs::{
    feldman, FeldmanVerifierSet, IdentifierPrimeField, ReadableShareSet, Share, ValueGroup,
};

/// A share of a secret produced by Shamir's Secret Sharing.
pub struct SecretShare {
    pub threshold: usize,
    /// 1-based index (evaluation point).
    pub index: u32,
    /// 32-byte big-endian scalar value.
    pub share: Vec<u8>,
}

/// A share of a secret together with Feldman VSS commitments.
pub struct VerifiableSecretShare {
    pub share: SecretShare,
    /// Compressed SEC1 pubkeys (33 bytes each), one per coefficient.
    pub proofs: Vec<Vec<u8>>,
}

// ---------------------------------------------------------------------------
// Type aliases for vsss-rs
// ---------------------------------------------------------------------------

type VsssShare = (IdentifierPrimeField<Scalar>, IdentifierPrimeField<Scalar>);
type VsssVerifier = ValueGroup<ProjectivePoint>;

// ---------------------------------------------------------------------------
// Byte serialization helpers
// ---------------------------------------------------------------------------

fn scalar_from_bytes(bytes: &[u8]) -> Result<Scalar, String> {
    let arr: [u8; 32] = bytes
        .try_into()
        .map_err(|_| format!("scalar must be 32 bytes, got {}", bytes.len()))?;
    Option::from(Scalar::from_repr(k256::FieldBytes::from(arr)))
        .ok_or_else(|| "invalid scalar encoding".to_string())
}

fn scalar_to_bytes(s: &Scalar) -> Vec<u8> {
    s.to_bytes().to_vec()
}

fn point_from_compressed(bytes: &[u8]) -> Result<ProjectivePoint, String> {
    let arr: [u8; 33] = bytes
        .try_into()
        .map_err(|_| format!("malformed public key: invalid length: {}", bytes.len()))?;
    Option::<AffinePoint>::from(AffinePoint::from_bytes(&arr.into()))
        .map(ProjectivePoint::from)
        .ok_or_else(|| "malformed public key: invalid encoding".to_string())
}

fn point_to_compressed(p: &ProjectivePoint) -> Vec<u8> {
    p.to_affine().to_encoded_point(true).as_bytes().to_vec()
}

// ---------------------------------------------------------------------------
// Conversion between our byte-oriented API and vsss-rs types
// ---------------------------------------------------------------------------

fn to_vsss_share(index: u32, share_bytes: &[u8]) -> Result<VsssShare, String> {
    let value = scalar_from_bytes(share_bytes)?;
    let id = IdentifierPrimeField(Scalar::from(index as u64));
    Ok((id, IdentifierPrimeField(value)))
}

fn scalar_to_index(s: &Scalar) -> u32 {
    let bytes = s.to_bytes();
    u32::from_be_bytes([bytes[28], bytes[29], bytes[30], bytes[31]])
}

/// Extract proofs (coefficient commitments) from the vsss-rs verifier set.
/// vsss-rs Vec layout: [generator, v0, v1, ..., v_{t-1}]
/// Our proofs format: [v0, v1, ..., v_{t-1}]
fn verifier_set_to_proofs(verifier_set: &Vec<VsssVerifier>) -> Vec<Vec<u8>> {
    <Vec<VsssVerifier> as FeldmanVerifierSet<VsssShare, VsssVerifier>>::verifiers(verifier_set)
        .iter()
        .map(|v| point_to_compressed(&v.0))
        .collect()
}

/// Reconstruct a vsss-rs verifier set Vec from our proofs format.
/// Prepends the standard generator to form [generator, v0, v1, ..., v_{t-1}].
fn proofs_to_verifier_set(proofs: &[Vec<u8>]) -> Result<Vec<VsssVerifier>, String> {
    let mut set = Vec::with_capacity(proofs.len() + 1);
    set.push(ValueGroup(ProjectivePoint::GENERATOR));
    for p in proofs {
        set.push(ValueGroup(point_from_compressed(p)?));
    }
    Ok(set)
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Split `secret` into `num_shares` shares with the given `threshold`.
///
/// `threshold` must be >= 2 and `num_shares` must be >= `threshold`.
pub fn split_secret(
    secret: &[u8],
    threshold: usize,
    num_shares: usize,
) -> Result<Vec<SecretShare>, String> {
    if threshold < 2 {
        return Err(format!("threshold must be >= 2, got {}", threshold));
    }
    if num_shares < threshold {
        return Err(format!(
            "num_shares must be >= threshold, got num_shares={}, threshold={}",
            num_shares, threshold
        ));
    }

    let secret_scalar = scalar_from_bytes(secret)?;

    let (shares, _): (Vec<VsssShare>, Vec<VsssVerifier>) = feldman::split_secret(
        threshold,
        num_shares,
        &IdentifierPrimeField(secret_scalar),
        None,
        OsRng,
    )
    .map_err(|e| format!("vsss split_secret failed: {e:?}"))?;

    Ok(shares
        .iter()
        .map(|s| SecretShare {
            threshold,
            index: scalar_to_index(&s.identifier().0),
            share: scalar_to_bytes(&s.value().0),
        })
        .collect())
}

/// Split `secret` into `num_shares` verifiable shares with Feldman proofs.
///
/// `threshold` must be >= 2 and `num_shares` must be >= `threshold`.
pub fn split_secret_with_proofs(
    secret: &[u8],
    threshold: usize,
    num_shares: usize,
) -> Result<Vec<VerifiableSecretShare>, String> {
    if threshold < 2 {
        return Err(format!("threshold must be >= 2, got {}", threshold));
    }
    if num_shares < threshold {
        return Err(format!(
            "num_shares must be >= threshold, got num_shares={}, threshold={}",
            num_shares, threshold
        ));
    }

    let secret_scalar = scalar_from_bytes(secret)?;

    let (shares, verifier_set): (Vec<VsssShare>, Vec<VsssVerifier>) = feldman::split_secret(
        threshold,
        num_shares,
        &IdentifierPrimeField(secret_scalar),
        None,
        OsRng,
    )
    .map_err(|e| format!("vsss split_secret failed: {e:?}"))?;

    let proofs = verifier_set_to_proofs(&verifier_set);

    Ok(shares
        .iter()
        .map(|s| VerifiableSecretShare {
            share: SecretShare {
                threshold,
                index: scalar_to_index(&s.identifier().0),
                share: scalar_to_bytes(&s.value().0),
            },
            proofs: proofs.clone(),
        })
        .collect())
}

/// Recover the secret from a set of shares using Lagrange interpolation.
pub fn recover_secret(shares: &[SecretShare]) -> Result<Vec<u8>, String> {
    if shares.is_empty() {
        return Err("no shares provided".to_string());
    }
    if shares.len() < shares[0].threshold {
        return Err("not enough shares to recover secret".to_string());
    }

    let vsss_shares: Vec<VsssShare> = shares
        .iter()
        .map(|s| to_vsss_share(s.index, &s.share))
        .collect::<Result<Vec<_>, _>>()?;

    let recovered: IdentifierPrimeField<Scalar> = vsss_shares
        .combine()
        .map_err(|e| format!("vsss combine failed: {e:?}"))?;

    Ok(scalar_to_bytes(&recovered.0))
}

/// Validate a verifiable secret share against its Feldman commitments.
pub fn validate_share(
    share: &[u8],
    index: u32,
    threshold: usize,
    proofs: &[Vec<u8>],
) -> Result<(), String> {
    if proofs.len() != threshold {
        return Err(format!(
            "invalid VSS proof length: expected {}, got {}",
            threshold,
            proofs.len()
        ));
    }

    let vsss_share = to_vsss_share(index, share)?;
    let verifier_set = proofs_to_verifier_set(proofs)?;

    verifier_set
        .verify_share(&vsss_share)
        .map_err(|_| "share is not valid".to_string())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    fn random_secret() -> Vec<u8> {
        scalar_to_bytes(&Scalar::random(&mut OsRng))
    }

    #[test]
    fn test_split_and_recover() {
        let secret = random_secret();
        let shares = split_secret(&secret, 3, 5).unwrap();
        let recovered = recover_secret(&shares[..3]).unwrap();
        assert_eq!(secret, recovered);
    }

    #[test]
    fn test_not_enough_shares() {
        let secret = random_secret();
        let shares = split_secret(&secret, 3, 5).unwrap();
        let err = recover_secret(&shares[..2]).unwrap_err();
        assert!(err.contains("not enough shares"));
    }

    #[test]
    fn test_vss_validation() {
        let secret = random_secret();
        let shares = split_secret_with_proofs(&secret, 3, 5).unwrap();

        for share in &shares {
            validate_share(
                &share.share.share,
                share.share.index,
                share.share.threshold,
                &share.proofs,
            )
            .unwrap();
        }

        let plain: Vec<SecretShare> = shares
            .iter()
            .map(|vs| SecretShare {
                threshold: vs.share.threshold,
                index: vs.share.index,
                share: vs.share.share.clone(),
            })
            .collect();
        let recovered = recover_secret(&plain[..3]).unwrap();
        assert_eq!(secret, recovered);
    }

    #[test]
    fn test_catch_bad_proof_encoding() {
        let secret = random_secret();
        let mut shares = split_secret_with_proofs(&secret, 3, 5).unwrap();

        // Corrupt first proof
        shares[0].proofs[0][0] ^= 0xFF;
        let err = validate_share(
            &shares[0].share.share,
            shares[0].share.index,
            shares[0].share.threshold,
            &shares[0].proofs,
        );
        assert!(err.is_err());
        shares[0].proofs[0][0] ^= 0xFF; // restore

        // Corrupt second proof
        shares[1].proofs[1][0] ^= 0xFF;
        let err = validate_share(
            &shares[1].share.share,
            shares[1].share.index,
            shares[1].share.threshold,
            &shares[1].proofs,
        );
        assert!(err.is_err());
    }

    #[test]
    fn test_catch_wrong_share() {
        let secret = random_secret();
        let shares = split_secret_with_proofs(&secret, 3, 5).unwrap();

        // Use share value from index 3 with proofs from index 2
        let err = validate_share(
            &shares[3].share.share,
            shares[2].share.index,
            shares[2].share.threshold,
            &shares[2].proofs,
        );
        assert!(err.is_err());
        assert!(err.unwrap_err().contains("share is not valid"));
    }

    #[test]
    fn test_catch_invalid_proof_length() {
        let secret = random_secret();
        let shares = split_secret_with_proofs(&secret, 3, 5).unwrap();

        let mut proofs = shares[0].proofs.clone();
        proofs.push(proofs[0].clone()); // extra proof
        let err = validate_share(
            &shares[0].share.share,
            shares[0].share.index,
            shares[0].share.threshold,
            &proofs,
        );
        assert!(err.is_err());
        assert!(err.unwrap_err().contains("invalid VSS proof length"));
    }

    #[test]
    fn test_bad_pubkey_len() {
        let secret = random_secret();
        let shares = split_secret_with_proofs(&secret, 3, 5).unwrap();

        let mut proofs = shares[0].proofs.clone();
        proofs[0] = proofs[0][..32].to_vec(); // truncate to 32 bytes
        let err = validate_share(
            &shares[0].share.share,
            shares[0].share.index,
            shares[0].share.threshold,
            &proofs,
        );
        assert!(err.is_err());
        assert!(err
            .unwrap_err()
            .contains("malformed public key: invalid length: 32"));
    }

    #[test]
    fn test_minimum_threshold() {
        let secret = random_secret();
        let shares = split_secret(&secret, 2, 2).unwrap();
        assert_eq!(shares.len(), 2);
        let recovered = recover_secret(&shares).unwrap();
        assert_eq!(secret, recovered);
    }

    #[test]
    fn test_all_shares_required() {
        let secret = random_secret();
        let shares = split_secret(&secret, 5, 5).unwrap();

        // n-1 shares should fail
        let err = recover_secret(&shares[..4]).unwrap_err();
        assert!(err.contains("not enough shares"));

        // all n shares should work
        let recovered = recover_secret(&shares).unwrap();
        assert_eq!(secret, recovered);
    }

    #[test]
    fn test_different_subsets_recover() {
        let secret = random_secret();
        let shares = split_secret(&secret, 3, 5).unwrap();

        // Try different 3-of-5 subsets
        let subsets: Vec<Vec<usize>> = vec![
            vec![0, 1, 2],
            vec![0, 1, 3],
            vec![0, 1, 4],
            vec![1, 2, 3],
            vec![2, 3, 4],
            vec![0, 3, 4],
        ];

        for subset in &subsets {
            let sub: Vec<SecretShare> = subset
                .iter()
                .map(|&i| SecretShare {
                    threshold: shares[i].threshold,
                    index: shares[i].index,
                    share: shares[i].share.clone(),
                })
                .collect();
            let recovered = recover_secret(&sub).unwrap();
            assert_eq!(secret, recovered, "failed for subset {:?}", subset);
        }
    }

    #[test]
    fn test_cross_language_vectors() {
        let secret_bytes = scalar_to_bytes(&Scalar::from(42u64));
        let shares = split_secret_with_proofs(&secret_bytes, 2, 3).unwrap();

        // All shares should validate
        for share in &shares {
            validate_share(
                &share.share.share,
                share.share.index,
                share.share.threshold,
                &share.proofs,
            )
            .unwrap();
        }

        // Any 2-of-3 subset should recover the secret
        let plain: Vec<SecretShare> = shares
            .iter()
            .map(|vs| SecretShare {
                threshold: vs.share.threshold,
                index: vs.share.index,
                share: vs.share.share.clone(),
            })
            .collect();

        let recovered = recover_secret(&plain[..2]).unwrap();
        assert_eq!(secret_bytes, recovered);
    }
}
