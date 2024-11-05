use std::collections::BTreeMap;
use std::collections::HashMap;

use frost_core::round1::Nonce;
use frost_core::round1::NonceCommitment;
use frost_secp256k1_tr::round1::SigningCommitments as FrostSigningCommitments;
use frost_secp256k1_tr::round1::SigningNonces as FrostSigningNonces;
use frost_secp256k1_tr::round2::SignatureShare;
use frost_secp256k1_tr::Identifier;
use frost_secp256k1_tr::SigningPackage;
use frost_secp256k1_tr::SigningParameters;
use frost_secp256k1_tr::SigningTarget;
use frost_secp256k1_tr::VerifyingKey;

use crate::dkg::hex_string_to_identifier;
use crate::server::frost::{SigningCommitment, SigningNonce};

pub fn frost_nonce_from_proto(nonce: &SigningNonce) -> Result<FrostSigningNonces, String> {
    let hiding_bytes = nonce
        .hiding
        .clone()
        .try_into()
        .map_err(|e| format!("Hiding is not 32 bytes: {:?}", e))?;
    let binding_bytes = nonce
        .binding
        .clone()
        .try_into()
        .map_err(|e| format!("Binding is not 32 bytes: {:?}", e))?;
    let hiding = Nonce::deserialize(hiding_bytes).map_err(|e| e.to_string())?;
    let binding = Nonce::deserialize(binding_bytes).map_err(|e| e.to_string())?;
    Ok(FrostSigningNonces::from_nonces(hiding, binding))
}

pub fn frost_commitments_from_proto(
    commitments: &SigningCommitment,
) -> Result<FrostSigningCommitments, String> {
    let hiding_bytes = commitments
        .hiding
        .clone()
        .try_into()
        .map_err(|e| format!("Hiding commitment is not 33 bytes: {:?}", e))?;
    let binding_bytes = commitments
        .binding
        .clone()
        .try_into()
        .map_err(|e| format!("Binding commitment is not 33 bytes: {:?}", e))?;
    let hiding_commitment =
        NonceCommitment::deserialize(hiding_bytes).map_err(|e| e.to_string())?;
    let binding_commitment =
        NonceCommitment::deserialize(binding_bytes).map_err(|e| e.to_string())?;
    Ok(FrostSigningCommitments::new(
        hiding_commitment,
        binding_commitment,
    ))
}

pub fn frost_signing_commiement_map_from_proto(
    map: &HashMap<String, SigningCommitment>,
) -> Result<BTreeMap<Identifier, FrostSigningCommitments>, String> {
    map.iter()
        .map(
            |(k, v)| -> Result<(Identifier, FrostSigningCommitments), String> {
                let identifier = hex_string_to_identifier(k)
                    .map_err(|e| format!("Failed to parse identifier: {}", e))?;
                let commitments = frost_commitments_from_proto(v)?;
                Ok((identifier, commitments))
            },
        )
        .collect::<Result<BTreeMap<_, _>, String>>()
}

pub fn verifying_key_from_bytes(bytes: Vec<u8>) -> Result<VerifyingKey, String> {
    let key_bytes: [u8; 33] = bytes
        .try_into()
        .map_err(|_| "Verifying key is not 33 bytes")?;
    VerifyingKey::deserialize(key_bytes).map_err(|e| e.to_string())
}

pub fn frost_build_signin_package(
    signing_commitments: BTreeMap<Identifier, FrostSigningCommitments>,
    message: &[u8],
) -> SigningPackage {
    let signing_target = SigningTarget::new(
        message,
        SigningParameters {
            tapscript_merkle_root: Some(vec![]),
        },
    );
    SigningPackage::new(signing_commitments, signing_target)
}

pub fn frost_signature_shares_from_proto(
    shares: &HashMap<String, Vec<u8>>,
) -> Result<BTreeMap<Identifier, SignatureShare>, String> {
    shares
        .iter()
        .map(|(k, v)| -> Result<(Identifier, SignatureShare), String> {
            let identifier = hex_string_to_identifier(k)
                .map_err(|e| format!("Failed to parse identifier: {}", e))?;
            let share = SignatureShare::deserialize(
                (*v).clone()
                    .try_into()
                    .map_err(|_| "Signature share is not 32 bytes")?,
            )
            .map_err(|e| e.to_string())?;
            Ok((identifier, share))
        })
        .collect()
}
