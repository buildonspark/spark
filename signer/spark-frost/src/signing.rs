use std::collections::BTreeMap;
use std::collections::BTreeSet;
use std::collections::HashMap;

use frost_core::round1::Nonce;
use frost_core::round1::NonceCommitment;
use frost_secp256k1_tr::keys::EvenY;
use frost_secp256k1_tr::keys::KeyPackage as FrostKeyPackage;
use frost_secp256k1_tr::keys::PublicKeyPackage;
use frost_secp256k1_tr::keys::SigningShare;
use frost_secp256k1_tr::keys::Tweak;
use frost_secp256k1_tr::keys::VerifyingShare;
use frost_secp256k1_tr::round1::SigningCommitments as FrostSigningCommitments;
use frost_secp256k1_tr::round1::SigningNonces as FrostSigningNonces;
use frost_secp256k1_tr::round2::SignatureShare;
use frost_secp256k1_tr::Identifier;
use frost_secp256k1_tr::SigningPackage;
use frost_secp256k1_tr::VerifyingKey;

use crate::hex_string_to_identifier;
use crate::proto::common::*;
use crate::proto::frost::*;

pub fn frost_nonce_from_proto(nonce: &SigningNonce) -> Result<FrostSigningNonces, String> {
    let hiding_bytes = nonce.hiding.as_slice();
    let binding_bytes = nonce.binding.as_slice();
    let hiding = Nonce::deserialize(hiding_bytes).map_err(|e| e.to_string())?;
    let binding = Nonce::deserialize(binding_bytes).map_err(|e| e.to_string())?;
    Ok(FrostSigningNonces::from_nonces(hiding, binding))
}

pub fn frost_commitments_from_proto(
    commitments: &SigningCommitment,
) -> Result<FrostSigningCommitments, String> {
    let hiding_bytes = commitments.hiding.as_slice();
    let binding_bytes = commitments.binding.as_slice();
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
    VerifyingKey::deserialize(bytes.as_slice()).map_err(|e| e.to_string())
}

pub fn frost_build_signin_package(
    signing_commitments: BTreeMap<Identifier, FrostSigningCommitments>,
    message: &[u8],
    signing_participants_groups: Option<Vec<BTreeSet<Identifier>>>,
    adaptor_public_key: &[u8],
) -> SigningPackage {
    let adaptor_public_key = VerifyingKey::deserialize(adaptor_public_key).ok();
    SigningPackage::new_with_adaptor(
        signing_commitments,
        signing_participants_groups,
        message,
        adaptor_public_key,
    )
}

pub fn frost_signature_shares_from_proto(
    shares: &HashMap<String, Vec<u8>>,
    user_identifier: Identifier,
    user_signature_share: &[u8],
) -> Result<BTreeMap<Identifier, SignatureShare>, String> {
    let mut shares_map = shares
        .iter()
        .map(|(k, v)| -> Result<(Identifier, SignatureShare), String> {
            let identifier = hex_string_to_identifier(k)
                .map_err(|e| format!("Failed to parse identifier: {}", e))?;
            let share = SignatureShare::deserialize(v).map_err(|e| e.to_string())?;
            Ok((identifier, share))
        })
        .collect::<Result<BTreeMap<_, _>, String>>()?;

    if !user_signature_share.is_empty() {
        shares_map.insert(
            user_identifier,
            SignatureShare::deserialize(user_signature_share).map_err(|e| e.to_string())?,
        );
    }
    Ok(shares_map)
}

pub fn frost_public_package_from_proto(
    public_shares: &HashMap<String, Vec<u8>>,
    user_identifier: Identifier,
    user_public_key: Vec<u8>,
    verifying_key: VerifyingKey,
) -> Result<PublicKeyPackage, String> {
    let mut final_shares = public_shares
        .iter()
        .map(|(k, v)| -> Result<(Identifier, VerifyingShare), String> {
            let identifier = hex_string_to_identifier(k)?;
            let share = VerifyingShare::deserialize(v).map_err(|e| e.to_string())?;
            Ok((identifier, share))
        })
        .collect::<Result<BTreeMap<_, _>, String>>()?;

    if !user_public_key.is_empty() {
        final_shares.insert(
            user_identifier,
            VerifyingShare::deserialize(user_public_key.as_slice()).map_err(|e| e.to_string())?,
        );
    }
    tracing::info!("final_shares: {:?}", final_shares);
    let public_key_package = PublicKeyPackage::new(final_shares, verifying_key);
    Ok(public_key_package)
}

pub fn frost_key_package_from_proto(
    key_package: &KeyPackage,
    identifier_override: Option<Identifier>,
    verifying_key: VerifyingKey,
    role: i32,
) -> Result<FrostKeyPackage, String> {
    let signing_share = SigningShare::deserialize(key_package.secret_share.as_slice())
        .map_err(|e| e.to_string())?;

    let verifying_share = VerifyingShare::deserialize(
        key_package
            .public_shares
            .get(&key_package.identifier)
            .ok_or("Verifying share is not found")?
            .as_slice(),
    )
    .map_err(|e| e.to_string())?;

    let identifier =
        identifier_override.unwrap_or(hex_string_to_identifier(&key_package.identifier)?);

    let result = FrostKeyPackage::new(
        identifier,
        signing_share,
        verifying_share,
        verifying_key,
        key_package.min_signers as u16,
    );

    if role == 1 {
        // For the user, we don't want to tweak the key with merkle root, but we need to make sure the key is even.
        // Then the total verifying key will need to tweak with the merkle root.
        let merkle_root = vec![];
        let result_tweaked = result.clone().tweak(Some(&merkle_root));
        let result_even_y = result.clone().into_even_y(Some(verifying_key.has_even_y()));
        let final_result = FrostKeyPackage::new(
            *result_even_y.identifier(),
            *result_even_y.signing_share(),
            *result_even_y.verifying_share(),
            *result_tweaked.verifying_key(),
            *result_tweaked.min_signers(),
        );
        Ok(final_result)
    } else {
        Ok(result)
    }
}

pub fn frost_nonce(req: &FrostNonceRequest) -> Result<FrostNonceResponse, String> {
    let mut results = Vec::new();

    for key_package in req.key_packages.iter() {
        let verifying_key = verifying_key_from_bytes(key_package.public_key.clone())
            .map_err(|e| format!("Failed to parse verifying key: {:?}", e))?;
        let key_package = frost_key_package_from_proto(key_package, None, verifying_key, 0)
            .map_err(|e| format!("Failed to parse key package: {:?}", e))?;

        let rng = &mut rand::thread_rng();
        let (nonce, commitment) =
            frost_secp256k1_tr::round1::commit(key_package.signing_share(), rng);

        let pb_nonce = SigningNonce {
            hiding: nonce.hiding().serialize().to_vec(),
            binding: nonce.binding().serialize().to_vec(),
        };

        let pb_commitment = SigningCommitment {
            hiding: commitment
                .hiding()
                .serialize()
                .map_err(|e| format!("Failed to serialize hiding commitment: {:?}", e))?,
            binding: commitment
                .binding()
                .serialize()
                .map_err(|e| format!("Failed to serialize binding commitment: {:?}", e))?,
        };

        results.push(SigningNonceResult {
            nonces: Some(pb_nonce),
            commitments: Some(pb_commitment),
        });
    }

    Ok(FrostNonceResponse { results })
}

pub fn sign_frost(req: &SignFrostRequest) -> Result<SignFrostResponse, String> {
    let mut results = HashMap::new();
    for job in req.signing_jobs.iter() {
        let mut commitments = frost_signing_commiement_map_from_proto(&job.commitments)
            .map_err(|e| format!("Failed to parse signing commitments: {:?}", e))?;

        let user_identifier =
            Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

        let mut signing_participants_groups = Vec::new();
        signing_participants_groups.push(commitments.keys().cloned().collect());

        tracing::debug!("User commitments: {:?}", job.user_commitments);

        if let Some(c) = &job.user_commitments {
            let user_commitments = frost_commitments_from_proto(c)
                .map_err(|e| format!("Failed to parse user commitments: {:?}", e))?;
            commitments.insert(user_identifier, user_commitments);
            signing_participants_groups.push(BTreeSet::from([user_identifier]));
        };
        tracing::debug!("There are {} commitments", commitments.len());

        let nonce = match &job.nonce {
            Some(nonce) => frost_nonce_from_proto(nonce)
                .map_err(|e| format!("Failed to parse nonce: {:?}", e))?,
            None => return Err("Nonce is required".to_string()),
        };

        let verifying_key = verifying_key_from_bytes(job.verifying_key.clone())
            .map_err(|e| format!("Failed to parse verifying key: {:?}", e))?;

        let identifier_override = match req.role {
            0 => None,
            1 => Some(user_identifier),
            _ => return Err("Invalid signing role".to_string()),
        };

        let key_package = match &job.key_package {
            Some(key_package) => frost_key_package_from_proto(
                key_package,
                identifier_override,
                verifying_key,
                req.role,
            )
            .map_err(|e| format!("Failed to parse key package: {:?}", e))?,
            None => return Err("Key package is required".to_string()),
        };

        let signing_package = frost_build_signin_package(
            commitments,
            &job.message,
            Some(signing_participants_groups),
            &job.adaptor_public_key,
        );

        tracing::info!("Building signing package completed");
        let tweak = vec![];
        let signature_share = match req.role {
            0 => frost_secp256k1_tr::round2::sign_with_tweak(
                &signing_package,
                &nonce,
                &key_package,
                Some(tweak.as_slice()),
            )
            .map_err(|e| format!("Failed to sign frost: {:?}", e))?,
            _ => frost_secp256k1_tr::round2::sign(&signing_package, &nonce, &key_package)
                .map_err(|e| format!("Failed to sign frost: {:?}", e))?,
        };
        tracing::info!("Signing frost completed");

        results.insert(
            job.job_id.clone(),
            SigningResult {
                signature_share: signature_share.serialize().to_vec(),
            },
        );
    }

    Ok(SignFrostResponse { results })
}

pub fn aggregate_frost(req: &AggregateFrostRequest) -> Result<AggregateFrostResponse, String> {
    let mut commitments = frost_signing_commiement_map_from_proto(&req.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {:?}", e))?;

    let mut signing_participants_groups = Vec::new();
    signing_participants_groups.push(commitments.keys().cloned().collect());

    let user_identifier =
        Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

    if let Some(c) = &req.user_commitments {
        let user_commitments = frost_commitments_from_proto(c)
            .map_err(|e| format!("Failed to parse user commitments: {:?}", e))?;
        commitments.insert(user_identifier, user_commitments);
    };

    let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {:?}", e))?;

    signing_participants_groups.push(BTreeSet::from([user_identifier]));

    let signing_package = frost_build_signin_package(
        commitments,
        &req.message,
        Some(signing_participants_groups),
        &req.adaptor_public_key,
    );

    let signature_shares = frost_signature_shares_from_proto(
        &req.signature_shares,
        user_identifier,
        &req.user_signature_share,
    )
    .map_err(|e| format!("Failed to parse signature shares: {:?}", e))?;

    let public_package = frost_public_package_from_proto(
        &req.public_shares,
        user_identifier,
        req.user_public_key.clone(),
        verifying_key,
    )
    .map_err(|e| format!("Failed to parse public package: {:?}", e))?;

    let tweak = vec![];

    tracing::info!("signing_package: {:?}", signing_package);
    tracing::info!("signature_shares: {:?}", signature_shares);
    tracing::info!("public_package: {:?}", public_package);

    let signature = frost_secp256k1_tr::aggregate_with_tweak(
        &signing_package,
        &signature_shares,
        &public_package,
        Some(&tweak),
    )
    .map_err(|e| format!("Failed to aggregate frost: {:?}", e))?;

    Ok(AggregateFrostResponse {
        signature: signature
            .serialize()
            .map_err(|e| format!("Failed to serialize signature: {:?}", e))?,
    })
}

pub fn validate_signature_share(req: &ValidateSignatureShareRequest) -> Result<(), String> {
    let identifier = match req.role {
        0 => hex_string_to_identifier(&req.identifier)
            .map_err(|e| format!("Failed to parse identifier: {:?}", e))?,
        1 => Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier"),
        _ => return Err("Invalid signing role".to_string()),
    };

    let signature_share = SignatureShare::deserialize(req.signature_share.as_slice())
        .map_err(|e| format!("Failed to parse signature share: {:?}", e))?;
    let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {:?}", e))?;

    let mut commitments = frost_signing_commiement_map_from_proto(&req.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {:?}", e))?;

    let user_identifier =
        Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

    let mut signing_participants_groups = Vec::new();
    signing_participants_groups.push(commitments.keys().cloned().collect());

    if let Some(c) = &req.user_commitments {
        let user_commitments = frost_commitments_from_proto(c)
            .map_err(|e| format!("Failed to parse user commitments: {:?}", e))?;
        commitments.insert(user_identifier, user_commitments);
        signing_participants_groups.push(BTreeSet::from([user_identifier]));
    }

    let public_share = VerifyingShare::deserialize(req.public_share.as_slice())
        .map_err(|e| format!("Failed to parse public share: {:?}", e))?;

    let adaptor_key: Vec<u8> = vec![];

    let signing_package = frost_build_signin_package(
        commitments,
        &req.message,
        Some(signing_participants_groups),
        &adaptor_key,
    );

    let dummy_signing_share = SigningShare::deserialize(&[0; 32])
        .map_err(|e| format!("Failed to parse dummy signing share: {:?}", e))?;

    let result = FrostKeyPackage::new(
        identifier,
        dummy_signing_share,
        public_share,
        verifying_key,
        2,
    );
    let merkle_root = vec![];
    let result_tweaked = result.clone().tweak(Some(&merkle_root));
    let result_even_y = result.clone().into_even_y(Some(verifying_key.has_even_y()));

    let verifying_share = match req.role {
        0 => result_tweaked.verifying_share(),
        1 => result_even_y.verifying_share(),
        _ => return Err("Invalid signing role".to_string()),
    };

    let verify_identifier = match req.role {
        0 => identifier,
        1 => user_identifier,
        _ => return Err("Invalid signing role".to_string()),
    };

    frost_secp256k1_tr::verify_signature_share(
        verify_identifier,
        verifying_share,
        &signature_share,
        &signing_package,
        result_tweaked.verifying_key(),
    )
    .map_err(|e| format!("Failed to verify signature share: {:?}", e))?;

    tracing::info!("Signature share is valid");

    Ok(())
}
