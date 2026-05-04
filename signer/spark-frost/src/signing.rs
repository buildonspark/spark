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
use rayon::prelude::*;

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
                    .map_err(|e| format!("Failed to parse identifier: {e}"))?;
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
                .map_err(|e| format!("Failed to parse identifier: {e}"))?;
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
    let public_key_package = PublicKeyPackage::new(final_shares, verifying_key, None);
    Ok(public_key_package)
}

pub fn parse_participant_groups_from_proto(
    groups: &[ParticipantGroup],
) -> Result<Vec<BTreeSet<Identifier>>, String> {
    groups
        .iter()
        .map(|group| {
            group
                .identifiers
                .iter()
                .map(|id| {
                    hex_string_to_identifier(id)
                        .map_err(|e| format!("Failed to parse group identifier: {e}"))
                })
                .collect::<Result<BTreeSet<Identifier>, String>>()
        })
        .collect()
}

pub fn frost_signature_shares_from_proto_v2(
    shares: &HashMap<String, Vec<u8>>,
) -> Result<BTreeMap<Identifier, SignatureShare>, String> {
    shares
        .iter()
        .map(|(k, v)| -> Result<(Identifier, SignatureShare), String> {
            let identifier = hex_string_to_identifier(k)
                .map_err(|e| format!("Failed to parse identifier: {e}"))?;
            let share = SignatureShare::deserialize(v).map_err(|e| e.to_string())?;
            Ok((identifier, share))
        })
        .collect::<Result<BTreeMap<_, _>, String>>()
}

pub fn frost_public_package_from_proto_v2(
    public_shares: &HashMap<String, Vec<u8>>,
    verifying_key: VerifyingKey,
) -> Result<PublicKeyPackage, String> {
    let shares = public_shares
        .iter()
        .map(|(k, v)| -> Result<(Identifier, VerifyingShare), String> {
            let identifier = hex_string_to_identifier(k)?;
            let share = VerifyingShare::deserialize(v).map_err(|e| e.to_string())?;
            Ok((identifier, share))
        })
        .collect::<Result<BTreeMap<_, _>, String>>()?;
    Ok(PublicKeyPackage::new(shares, verifying_key, None))
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

pub fn frost_key_package_from_proto_v2(
    key_package: &KeyPackage,
    verifying_key: VerifyingKey,
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

    let identifier = hex_string_to_identifier(&key_package.identifier)?;

    // No even-Y adjustment here — callers provide properly adjusted key
    // packages. SE callers pre-apply into_even_y(combined_vk) during key
    // setup. User callers pass raw shares; sign_with_tweak handles even-Y
    // internally via its tweak() call.
    Ok(FrostKeyPackage::new(
        identifier,
        signing_share,
        verifying_share,
        verifying_key,
        key_package.min_signers as u16,
    ))
}

pub fn frost_nonce(req: &FrostNonceRequest) -> Result<FrostNonceResponse, String> {
    let mut results = Vec::new();

    for key_package in req.key_packages.iter() {
        let verifying_key = verifying_key_from_bytes(key_package.public_key.clone())
            .map_err(|e| format!("Failed to parse verifying key: {e:?}"))?;
        let key_package = frost_key_package_from_proto(key_package, None, verifying_key, 0)
            .map_err(|e| format!("Failed to parse key package: {e:?}"))?;

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
                .map_err(|e| format!("Failed to serialize hiding commitment: {e:?}"))?,
            binding: commitment
                .binding()
                .serialize()
                .map_err(|e| format!("Failed to serialize binding commitment: {e:?}"))?,
        };

        results.push(SigningNonceResult {
            nonces: Some(pb_nonce),
            commitments: Some(pb_commitment),
        });
    }

    Ok(FrostNonceResponse { results })
}

fn sign_frost_job(job: &FrostSigningJob, req: &SignFrostRequest) -> Result<SignatureShare, String> {
    let mut commitments = frost_signing_commiement_map_from_proto(&job.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {e:?}"))?;

    let user_identifier =
        Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

    let mut signing_participants_groups = Vec::new();
    signing_participants_groups.push(commitments.keys().cloned().collect());

    tracing::debug!("User commitments: {:?}", job.user_commitments);

    if let Some(c) = &job.user_commitments {
        let user_commitments = frost_commitments_from_proto(c)
            .map_err(|e| format!("Failed to parse user commitments: {e:?}"))?;
        commitments.insert(user_identifier, user_commitments);
        signing_participants_groups.push(BTreeSet::from([user_identifier]));
    };
    tracing::debug!("There are {} commitments", commitments.len());

    let nonce = match &job.nonce {
        Some(nonce) => {
            frost_nonce_from_proto(nonce).map_err(|e| format!("Failed to parse nonce: {e:?}"))?
        }
        None => return Err("Nonce is required".to_string()),
    };

    let verifying_key = verifying_key_from_bytes(job.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {e:?}"))?;

    let identifier_override = match req.role {
        0 => None,
        1 => Some(user_identifier),
        _ => return Err("Invalid signing role".to_string()),
    };

    let key_package = match &job.key_package {
        Some(key_package) => {
            frost_key_package_from_proto(key_package, identifier_override, verifying_key, req.role)
                .map_err(|e| format!("Failed to parse key package: {e:?}"))?
        }
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
        .map_err(|e| format!("Failed to sign frost: {e:?}"))?,
        _ => frost_secp256k1_tr::round2::sign(&signing_package, &nonce, &key_package)
            .map_err(|e| format!("Failed to sign frost: {e:?}"))?,
    };
    tracing::info!("Signing frost completed");

    Ok(signature_share)
}

pub fn sign_frost(req: &SignFrostRequest) -> Result<SignFrostResponse, String> {
    let results: HashMap<String, SigningResult> = req
        .signing_jobs
        .par_iter()
        .map(|job| {
            let signature_share =
                sign_frost_job(job, req).map_err(|e| format!("Failed to sign frost: {e:?}"))?;

            Ok((
                job.job_id.clone(),
                SigningResult {
                    signature_share: signature_share.serialize().to_vec(),
                },
            ))
        })
        .collect::<Result<HashMap<String, SigningResult>, String>>()?;

    Ok(SignFrostResponse { results })
}

pub fn sign_frost_serial(req: &SignFrostRequest) -> Result<SignFrostResponse, String> {
    let mut results = HashMap::new();
    for job in req.signing_jobs.iter() {
        let signature_share =
            sign_frost_job(job, req).map_err(|e| format!("Failed to sign frost: {e:?}"))?;
        results.insert(
            job.job_id.clone(),
            SigningResult {
                signature_share: signature_share.serialize().to_vec(),
            },
        );
    }
    Ok(SignFrostResponse { results })
}

fn sign_frost_job_v2(job: &FrostSigningJobV2) -> Result<SignatureShare, String> {
    let commitments = frost_signing_commiement_map_from_proto(&job.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {e:?}"))?;

    let participant_groups = parse_participant_groups_from_proto(&job.participant_groups)?;

    let nonce = match &job.nonce {
        Some(nonce) => {
            frost_nonce_from_proto(nonce).map_err(|e| format!("Failed to parse nonce: {e:?}"))?
        }
        None => return Err("Nonce is required".to_string()),
    };

    let verifying_key = verifying_key_from_bytes(job.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {e:?}"))?;

    let key_package = match &job.key_package {
        Some(key_package) => frost_key_package_from_proto_v2(key_package, verifying_key)
            .map_err(|e| format!("Failed to parse key package: {e:?}"))?,
        None => return Err("Key package is required".to_string()),
    };

    let signing_package = frost_build_signin_package(
        commitments,
        &job.message,
        Some(participant_groups),
        &job.adaptor_public_key,
    );

    tracing::info!("V2: Building signing package completed");

    let signature_share = if job.use_tweak {
        let tweak = vec![];
        frost_secp256k1_tr::round2::sign_with_tweak(
            &signing_package,
            &nonce,
            &key_package,
            Some(tweak.as_slice()),
        )
        .map_err(|e| format!("Failed to sign frost (with tweak): {e:?}"))?
    } else {
        frost_secp256k1_tr::round2::sign(&signing_package, &nonce, &key_package)
            .map_err(|e| format!("Failed to sign frost: {e:?}"))?
    };

    tracing::info!("V2: Signing frost completed");
    Ok(signature_share)
}

pub fn sign_frost_v2(req: &SignFrostRequestV2) -> Result<SignFrostResponse, String> {
    let results: HashMap<String, SigningResult> = req
        .signing_jobs
        .par_iter()
        .map(|job| {
            let signature_share =
                sign_frost_job_v2(job).map_err(|e| format!("Failed to sign frost v2: {e:?}"))?;
            Ok((
                job.job_id.clone(),
                SigningResult {
                    signature_share: signature_share.serialize().to_vec(),
                },
            ))
        })
        .collect::<Result<HashMap<String, SigningResult>, String>>()?;
    Ok(SignFrostResponse { results })
}

pub fn aggregate_frost_v2(req: &AggregateFrostRequestV2) -> Result<AggregateFrostResponse, String> {
    let commitments = frost_signing_commiement_map_from_proto(&req.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {e:?}"))?;

    let participant_groups = parse_participant_groups_from_proto(&req.participant_groups)?;

    let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {e:?}"))?;

    let signing_package = frost_build_signin_package(
        commitments,
        &req.message,
        Some(participant_groups),
        &req.adaptor_public_key,
    );

    let signature_shares = frost_signature_shares_from_proto_v2(&req.signature_shares)
        .map_err(|e| format!("Failed to parse signature shares: {e:?}"))?;

    let public_package = frost_public_package_from_proto_v2(&req.public_shares, verifying_key)
        .map_err(|e| format!("Failed to parse public package: {e:?}"))?;

    let tweak = vec![];

    let signature = frost_secp256k1_tr::aggregate_with_tweak(
        &signing_package,
        &signature_shares,
        &public_package,
        Some(&tweak),
    )
    .map_err(|e| format!("Failed to aggregate frost: {e:?}"))?;

    Ok(AggregateFrostResponse {
        signature: signature
            .serialize()
            .map_err(|e| format!("Failed to serialize signature: {e:?}"))?,
    })
}

pub fn validate_signature_share_v2(req: &ValidateSignatureShareRequestV2) -> Result<(), String> {
    let identifier = hex_string_to_identifier(&req.identifier)
        .map_err(|e| format!("Failed to parse identifier: {e:?}"))?;

    let signature_share = SignatureShare::deserialize(req.signature_share.as_slice())
        .map_err(|e| format!("Failed to parse signature share: {e:?}"))?;

    let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {e:?}"))?;

    let commitments = frost_signing_commiement_map_from_proto(&req.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {e:?}"))?;

    let participant_groups = parse_participant_groups_from_proto(&req.participant_groups)?;

    let public_share = VerifyingShare::deserialize(req.public_share.as_slice())
        .map_err(|e| format!("Failed to parse public share: {e:?}"))?;

    let signing_package =
        frost_build_signin_package(commitments, &req.message, Some(participant_groups), &[]);

    frost_secp256k1_tr::verify_signature_share(
        identifier,
        &public_share,
        &signature_share,
        &signing_package,
        &verifying_key,
    )
    .map_err(|e| format!("Failed to verify signature share: {e:?}"))?;

    tracing::info!("V2: Signature share is valid");
    Ok(())
}

pub fn aggregate_frost(req: &AggregateFrostRequest) -> Result<AggregateFrostResponse, String> {
    let mut commitments = frost_signing_commiement_map_from_proto(&req.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {e:?}"))?;

    let mut signing_participants_groups = Vec::new();
    signing_participants_groups.push(commitments.keys().cloned().collect());

    let user_identifier =
        Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

    if let Some(c) = &req.user_commitments {
        let user_commitments = frost_commitments_from_proto(c)
            .map_err(|e| format!("Failed to parse user commitments: {e:?}"))?;
        commitments.insert(user_identifier, user_commitments);
    };

    let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {e:?}"))?;

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
    .map_err(|e| format!("Failed to parse signature shares: {e:?}"))?;

    let public_package = frost_public_package_from_proto(
        &req.public_shares,
        user_identifier,
        req.user_public_key.clone(),
        verifying_key,
    )
    .map_err(|e| format!("Failed to parse public package: {e:?}"))?;

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
    .map_err(|e| format!("Failed to aggregate frost: {e:?}"))?;

    Ok(AggregateFrostResponse {
        signature: signature
            .serialize()
            .map_err(|e| format!("Failed to serialize signature: {e:?}"))?,
    })
}

pub fn validate_signature_share(req: &ValidateSignatureShareRequest) -> Result<(), String> {
    let identifier = match req.role {
        0 => hex_string_to_identifier(&req.identifier)
            .map_err(|e| format!("Failed to parse identifier: {e:?}"))?,
        1 => Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier"),
        _ => return Err("Invalid signing role".to_string()),
    };

    let signature_share = SignatureShare::deserialize(req.signature_share.as_slice())
        .map_err(|e| format!("Failed to parse signature share: {e:?}"))?;
    let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
        .map_err(|e| format!("Failed to parse verifying key: {e:?}"))?;

    let mut commitments = frost_signing_commiement_map_from_proto(&req.commitments)
        .map_err(|e| format!("Failed to parse signing commitments: {e:?}"))?;

    let user_identifier =
        Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

    let mut signing_participants_groups = Vec::new();
    signing_participants_groups.push(commitments.keys().cloned().collect());

    if let Some(c) = &req.user_commitments {
        let user_commitments = frost_commitments_from_proto(c)
            .map_err(|e| format!("Failed to parse user commitments: {e:?}"))?;
        commitments.insert(user_identifier, user_commitments);
        signing_participants_groups.push(BTreeSet::from([user_identifier]));
    }

    let public_share = VerifyingShare::deserialize(req.public_share.as_slice())
        .map_err(|e| format!("Failed to parse public share: {e:?}"))?;

    let adaptor_key: Vec<u8> = vec![];

    let signing_package = frost_build_signin_package(
        commitments,
        &req.message,
        Some(signing_participants_groups),
        &adaptor_key,
    );

    let dummy_signing_share = SigningShare::deserialize(&[0; 32])
        .map_err(|e| format!("Failed to parse dummy signing share: {e:?}"))?;

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
    .map_err(|e| format!("Failed to verify signature share: {e:?}"))?;

    tracing::info!("Signature share is valid");

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use frost_secp256k1_tr::{
        self as frost,
        keys::{EvenY, KeyPackage as FrostKP, PublicKeyPackage, Tweak},
        Identifier, VerifyingKey,
    };
    use rand::thread_rng;
    use std::collections::{BTreeMap, HashMap};

    fn id_to_hex(id: &Identifier) -> String {
        hex::encode(id.serialize())
    }

    fn commitment_to_proto(
        c: &frost_secp256k1_tr::round1::SigningCommitments,
    ) -> SigningCommitment {
        SigningCommitment {
            hiding: c.hiding().serialize().unwrap(),
            binding: c.binding().serialize().unwrap(),
        }
    }

    fn key_package_to_proto(kp: &FrostKP, pubkey_pkg: &PublicKeyPackage) -> KeyPackage {
        let mut public_shares = HashMap::new();
        for (id, share) in pubkey_pkg.verifying_shares() {
            public_shares.insert(id_to_hex(id), share.serialize().unwrap().to_vec());
        }
        KeyPackage {
            identifier: id_to_hex(kp.identifier()),
            secret_share: kp.signing_share().serialize().to_vec(),
            public_shares,
            public_key: kp.verifying_key().serialize().unwrap().to_vec(),
            min_signers: *kp.min_signers() as u32,
        }
    }

    /// Generate SE + user key material for two-group signing.
    ///
    /// SE key packages get into_even_y(combined_vk) and VK = tweaked_vk.
    /// User key packages are RAW (no even-y) with VK = combined_vk.
    /// sign_with_tweak handles even-Y + tweak internally for user participants.
    fn generate_two_group_keys(
        se_max: u16,
        se_min: u16,
        user_max: u16,
        user_min: u16,
    ) -> (
        BTreeMap<Identifier, FrostKP>,
        PublicKeyPackage,
        BTreeMap<Identifier, FrostKP>,
        PublicKeyPackage,
        VerifyingKey,
        VerifyingKey,
    ) {
        let mut rng = thread_rng();

        let (se_shares, se_pubkey_pkg) = frost::keys::generate_with_dealer(
            se_max,
            se_min,
            frost::keys::IdentifierList::Default,
            &mut rng,
        )
        .unwrap();

        // For user group: if 1-of-1, create key directly (generate_with_dealer
        // requires min_signers >= 2). Otherwise use generate_with_dealer.
        let (user_key_packages_raw, user_pubkey_pkg): (
            BTreeMap<Identifier, FrostKP>,
            PublicKeyPackage,
        ) = if user_max == 1 && user_min == 1 {
            let user_id = Identifier::try_from(101u16).unwrap();
            let user_key = frost_secp256k1_tr::SigningKey::new(&mut rng);
            let user_vk = VerifyingKey::from(&user_key);
            let user_signing_share =
                frost_secp256k1_tr::keys::SigningShare::new(user_key.to_scalar());
            let user_verifying_share =
                frost_secp256k1_tr::keys::VerifyingShare::new(user_vk.to_element());
            // Use a dummy VK; we'll replace it below when we know the combined VK.
            let kp = FrostKP::new(
                user_id,
                user_signing_share,
                user_verifying_share,
                user_vk, // placeholder
                1,
            );
            let mut shares = BTreeMap::new();
            shares.insert(user_id, user_verifying_share);
            let pkg = PublicKeyPackage::new(shares, user_vk, None);
            let mut kps = BTreeMap::new();
            kps.insert(user_id, kp);
            (kps, pkg)
        } else {
            let user_ids: Vec<Identifier> = (101..=100 + user_max)
                .map(|i| Identifier::try_from(i).unwrap())
                .collect();
            let (user_shares, user_pkg) = frost::keys::generate_with_dealer(
                user_max,
                user_min,
                frost::keys::IdentifierList::Custom(&user_ids),
                &mut rng,
            )
            .unwrap();
            let kps: BTreeMap<Identifier, FrostKP> = user_shares
                .into_iter()
                .map(|(id, ss)| (id, FrostKP::try_from(ss).unwrap()))
                .collect();
            (kps, user_pkg)
        };

        // Combined verifying key
        let combined_vk_element = se_pubkey_pkg.verifying_key().to_element()
            + user_pubkey_pkg.verifying_key().to_element();
        let combined_vk = VerifyingKey::new(combined_vk_element);

        let merkle_root = vec![];
        let mut all_raw_shares = se_pubkey_pkg.verifying_shares().clone();
        all_raw_shares.extend(user_pubkey_pkg.verifying_shares().clone());
        let combined_pubkey_pkg = PublicKeyPackage::new(all_raw_shares, combined_vk, None);
        let tweaked_combined_pkg = combined_pubkey_pkg.clone().tweak(Some(&merkle_root));
        let tweaked_combined_vk = *tweaked_combined_pkg.verifying_key();

        // SE key packages: even-Y adjusted based on combined_vk, VK = tweaked_vk
        let se_key_packages: BTreeMap<Identifier, FrostKP> = se_shares
            .into_iter()
            .map(|(id, secret_share)| {
                let kp = FrostKP::try_from(secret_share).unwrap();
                let kp_even = kp.into_even_y(Some(combined_vk.has_even_y()));
                let kp_final = FrostKP::new(
                    id,
                    *kp_even.signing_share(),
                    *kp_even.verifying_share(),
                    tweaked_combined_vk,
                    *kp_even.min_signers(),
                );
                (id, kp_final)
            })
            .collect();

        // User key packages: RAW (no even-y), VK = combined_vk
        // sign_with_tweak handles even-Y internally via tweak().
        let user_key_packages: BTreeMap<Identifier, FrostKP> = user_key_packages_raw
            .into_iter()
            .map(|(id, kp)| {
                let kp_final = FrostKP::new(
                    id,
                    *kp.signing_share(),
                    *kp.verifying_share(),
                    combined_vk,
                    *kp.min_signers(),
                );
                (id, kp_final)
            })
            .collect();

        // Build public key packages for proto serialization
        let mut se_pub_shares = BTreeMap::new();
        for (id, kp) in &se_key_packages {
            se_pub_shares.insert(*id, *kp.verifying_share());
        }
        let se_final_pubkey = PublicKeyPackage::new(se_pub_shares, tweaked_combined_vk, None);

        let mut user_pub_shares = BTreeMap::new();
        for (id, kp) in &user_key_packages {
            user_pub_shares.insert(*id, *kp.verifying_share());
        }
        let user_final_pubkey = PublicKeyPackage::new(user_pub_shares, combined_vk, None);

        (
            se_key_packages,
            se_final_pubkey,
            user_key_packages,
            user_final_pubkey,
            combined_vk,
            tweaked_combined_vk,
        )
    }

    /// Run a full V2 two-group sign + aggregate cycle via proto messages.
    #[allow(clippy::too_many_arguments)]
    fn run_v2_sign_and_aggregate(
        se_key_packages: &BTreeMap<Identifier, FrostKP>,
        se_pubkey_pkg: &PublicKeyPackage,
        user_key_packages: &BTreeMap<Identifier, FrostKP>,
        user_pubkey_pkg: &PublicKeyPackage,
        se_min: u16,
        user_min: u16,
        combined_vk: &VerifyingKey,
        tweaked_vk: &VerifyingKey,
        message: &[u8],
    ) -> Vec<u8> {
        let mut rng = thread_rng();

        let se_signers: Vec<_> = se_key_packages
            .keys()
            .take(se_min as usize)
            .cloned()
            .collect();
        let user_signers: Vec<_> = user_key_packages
            .keys()
            .take(user_min as usize)
            .cloned()
            .collect();

        let mut nonces_map: BTreeMap<Identifier, frost_secp256k1_tr::round1::SigningNonces> =
            BTreeMap::new();
        let mut commitments_map: BTreeMap<
            Identifier,
            frost_secp256k1_tr::round1::SigningCommitments,
        > = BTreeMap::new();

        for id in se_signers.iter().chain(user_signers.iter()) {
            let kp = se_key_packages
                .get(id)
                .or_else(|| user_key_packages.get(id))
                .unwrap();
            let (nonce, commitment) = frost::round1::commit(kp.signing_share(), &mut rng);
            nonces_map.insert(*id, nonce);
            commitments_map.insert(*id, commitment);
        }

        let proto_commitments: HashMap<String, SigningCommitment> = commitments_map
            .iter()
            .map(|(id, c)| (id_to_hex(id), commitment_to_proto(c)))
            .collect();

        let se_group = ParticipantGroup {
            identifiers: se_signers.iter().map(id_to_hex).collect(),
        };
        let user_group = ParticipantGroup {
            identifiers: user_signers.iter().map(id_to_hex).collect(),
        };

        let mut all_signature_shares: HashMap<String, Vec<u8>> = HashMap::new();

        // SE participants sign (use_tweak=false, verifying_key=tweaked)
        for id in &se_signers {
            let kp = &se_key_packages[id];
            let nonce = &nonces_map[id];
            let proto_kp = key_package_to_proto(kp, se_pubkey_pkg);

            let job = FrostSigningJobV2 {
                job_id: id_to_hex(id),
                message: message.to_vec(),
                key_package: Some(proto_kp),
                verifying_key: tweaked_vk.serialize().unwrap().to_vec(),
                nonce: Some(SigningNonce {
                    hiding: nonce.hiding().serialize().to_vec(),
                    binding: nonce.binding().serialize().to_vec(),
                }),
                commitments: proto_commitments.clone(),
                participant_groups: vec![se_group.clone(), user_group.clone()],
                adaptor_public_key: vec![],
                use_tweak: false,
            };

            let req = SignFrostRequestV2 {
                signing_jobs: vec![job],
            };
            let resp = sign_frost_v2(&req).unwrap();
            let share = resp.results.get(&id_to_hex(id)).unwrap();
            all_signature_shares.insert(id_to_hex(id), share.signature_share.clone());
        }

        // User participants sign (use_tweak=true, verifying_key=untweaked)
        for id in &user_signers {
            let kp = &user_key_packages[id];
            let nonce = &nonces_map[id];
            let proto_kp = key_package_to_proto(kp, user_pubkey_pkg);

            let job = FrostSigningJobV2 {
                job_id: id_to_hex(id),
                message: message.to_vec(),
                key_package: Some(proto_kp),
                verifying_key: combined_vk.serialize().unwrap().to_vec(),
                nonce: Some(SigningNonce {
                    hiding: nonce.hiding().serialize().to_vec(),
                    binding: nonce.binding().serialize().to_vec(),
                }),
                commitments: proto_commitments.clone(),
                participant_groups: vec![se_group.clone(), user_group.clone()],
                adaptor_public_key: vec![],
                use_tweak: true,
            };

            let req = SignFrostRequestV2 {
                signing_jobs: vec![job],
            };
            let resp = sign_frost_v2(&req).unwrap();
            let share = resp.results.get(&id_to_hex(id)).unwrap();
            all_signature_shares.insert(id_to_hex(id), share.signature_share.clone());
        }

        let merkle_root = vec![];
        let mut all_public_shares: HashMap<String, Vec<u8>> = HashMap::new();

        for id in &se_signers {
            let kp = &se_key_packages[id];
            let share = kp.verifying_share();
            all_public_shares.insert(id_to_hex(id), share.serialize().unwrap().to_vec());
        }

        for id in &user_signers {
            let kp = &user_key_packages[id];
            let tweaked_kp = kp.clone().tweak(Some(&merkle_root));
            all_public_shares.insert(
                id_to_hex(id),
                tweaked_kp.verifying_share().serialize().unwrap().to_vec(),
            );
        }

        let agg_req = AggregateFrostRequestV2 {
            message: message.to_vec(),
            signature_shares: all_signature_shares,
            public_shares: all_public_shares,
            verifying_key: combined_vk.serialize().unwrap().to_vec(),
            commitments: proto_commitments,
            participant_groups: vec![se_group, user_group],
            adaptor_public_key: vec![],
        };
        let agg_resp = aggregate_frost_v2(&agg_req).unwrap();
        agg_resp.signature
    }

    fn verify_signature(
        se_pub: &PublicKeyPackage,
        user_pub: &PublicKeyPackage,
        combined_vk: &VerifyingKey,
        message: &[u8],
        sig: &[u8],
    ) {
        let merkle_root = vec![];
        let mut all_shares = se_pub.verifying_shares().clone();
        all_shares.extend(user_pub.verifying_shares().clone());
        let combined_pkg = PublicKeyPackage::new(all_shares, *combined_vk, None);
        let tweaked_pkg = combined_pkg.tweak(Some(&merkle_root));
        let frost_sig = frost_secp256k1_tr::Signature::deserialize(sig).unwrap();
        tweaked_pkg
            .verifying_key()
            .verify(message, &frost_sig)
            .expect("V2 signature should verify against tweaked VK");
    }

    #[test]
    fn test_v2_two_group_se_3of5_user_single() {
        let (se_kps, se_pub, user_kps, user_pub, combined_vk, tweaked_vk) =
            generate_two_group_keys(5, 3, 1, 1);

        let message = b"test two group SE 3of5 + single user";
        let sig = run_v2_sign_and_aggregate(
            &se_kps,
            &se_pub,
            &user_kps,
            &user_pub,
            3,
            1,
            &combined_vk,
            &tweaked_vk,
            message,
        );

        verify_signature(&se_pub, &user_pub, &combined_vk, message, &sig);
    }

    #[test]
    fn test_v2_two_group_se_3of5_user_2of3_mpc() {
        let (se_kps, se_pub, user_kps, user_pub, combined_vk, tweaked_vk) =
            generate_two_group_keys(5, 3, 3, 2);

        let message = b"test two group SE 3of5 + user MPC 2of3";
        let sig = run_v2_sign_and_aggregate(
            &se_kps,
            &se_pub,
            &user_kps,
            &user_pub,
            3,
            2,
            &combined_vk,
            &tweaked_vk,
            message,
        );

        verify_signature(&se_pub, &user_pub, &combined_vk, message, &sig);
    }

    #[test]
    fn test_v2_two_group_se_2of3_user_3of5_mpc() {
        let (se_kps, se_pub, user_kps, user_pub, combined_vk, tweaked_vk) =
            generate_two_group_keys(3, 2, 5, 3);

        let message = b"test SE 2of3 + user MPC 3of5";
        let sig = run_v2_sign_and_aggregate(
            &se_kps,
            &se_pub,
            &user_kps,
            &user_pub,
            2,
            3,
            &combined_vk,
            &tweaked_vk,
            message,
        );

        verify_signature(&se_pub, &user_pub, &combined_vk, message, &sig);
    }

    #[test]
    fn test_v2_two_group_minimal_2of2_plus_2of2() {
        let (se_kps, se_pub, user_kps, user_pub, combined_vk, tweaked_vk) =
            generate_two_group_keys(2, 2, 2, 2);

        let message = b"minimal 2of2 + 2of2";
        let sig = run_v2_sign_and_aggregate(
            &se_kps,
            &se_pub,
            &user_kps,
            &user_pub,
            2,
            2,
            &combined_vk,
            &tweaked_vk,
            message,
        );

        verify_signature(&se_pub, &user_pub, &combined_vk, message, &sig);
    }

    #[test]
    fn test_v2_validate_individual_shares() {
        let (se_kps, se_pub, user_kps, user_pub, combined_vk, tweaked_vk) =
            generate_two_group_keys(3, 2, 3, 2);

        let mut rng = thread_rng();
        let message = b"validate individual shares";

        let se_signers: Vec<_> = se_kps.keys().take(2).cloned().collect();
        let user_signers: Vec<_> = user_kps.keys().take(2).cloned().collect();

        let mut nonces_map = BTreeMap::new();
        let mut commitments_map = BTreeMap::new();
        for id in se_signers.iter().chain(user_signers.iter()) {
            let kp = se_kps.get(id).or_else(|| user_kps.get(id)).unwrap();
            let (nonce, commitment) = frost::round1::commit(kp.signing_share(), &mut rng);
            nonces_map.insert(*id, nonce);
            commitments_map.insert(*id, commitment);
        }

        let proto_commitments: HashMap<String, SigningCommitment> = commitments_map
            .iter()
            .map(|(id, c)| (id_to_hex(id), commitment_to_proto(c)))
            .collect();

        let se_group = ParticipantGroup {
            identifiers: se_signers.iter().map(id_to_hex).collect(),
        };
        let user_group = ParticipantGroup {
            identifiers: user_signers.iter().map(id_to_hex).collect(),
        };

        // Sign and validate each SE share
        for id in &se_signers {
            let kp = &se_kps[id];
            let nonce = &nonces_map[id];
            let proto_kp = key_package_to_proto(kp, &se_pub);

            let job = FrostSigningJobV2 {
                job_id: id_to_hex(id),
                message: message.to_vec(),
                key_package: Some(proto_kp),
                verifying_key: tweaked_vk.serialize().unwrap().to_vec(),
                nonce: Some(SigningNonce {
                    hiding: nonce.hiding().serialize().to_vec(),
                    binding: nonce.binding().serialize().to_vec(),
                }),
                commitments: proto_commitments.clone(),
                participant_groups: vec![se_group.clone(), user_group.clone()],
                adaptor_public_key: vec![],
                use_tweak: false,
            };
            let resp = sign_frost_v2(&SignFrostRequestV2 {
                signing_jobs: vec![job],
            })
            .unwrap();
            let share_bytes = &resp.results.get(&id_to_hex(id)).unwrap().signature_share;

            let validate_req = ValidateSignatureShareRequestV2 {
                identifier: id_to_hex(id),
                message: message.to_vec(),
                signature_share: share_bytes.clone(),
                public_share: kp.verifying_share().serialize().unwrap().to_vec(),
                verifying_key: tweaked_vk.serialize().unwrap().to_vec(),
                commitments: proto_commitments.clone(),
                participant_groups: vec![se_group.clone(), user_group.clone()],
            };
            validate_signature_share_v2(&validate_req).expect("SE signature share should validate");
        }

        // Sign and validate each user share
        let merkle_root = vec![];
        for id in &user_signers {
            let kp = &user_kps[id];
            let nonce = &nonces_map[id];
            let proto_kp = key_package_to_proto(kp, &user_pub);

            let job = FrostSigningJobV2 {
                job_id: id_to_hex(id),
                message: message.to_vec(),
                key_package: Some(proto_kp),
                verifying_key: combined_vk.serialize().unwrap().to_vec(),
                nonce: Some(SigningNonce {
                    hiding: nonce.hiding().serialize().to_vec(),
                    binding: nonce.binding().serialize().to_vec(),
                }),
                commitments: proto_commitments.clone(),
                participant_groups: vec![se_group.clone(), user_group.clone()],
                adaptor_public_key: vec![],
                use_tweak: true,
            };
            let resp = sign_frost_v2(&SignFrostRequestV2 {
                signing_jobs: vec![job],
            })
            .unwrap();
            let share_bytes = &resp.results.get(&id_to_hex(id)).unwrap().signature_share;

            let tweaked_kp = kp.clone().tweak(Some(&merkle_root));
            let validate_req = ValidateSignatureShareRequestV2 {
                identifier: id_to_hex(id),
                message: message.to_vec(),
                signature_share: share_bytes.clone(),
                public_share: tweaked_kp.verifying_share().serialize().unwrap().to_vec(),
                verifying_key: tweaked_vk.serialize().unwrap().to_vec(),
                commitments: proto_commitments.clone(),
                participant_groups: vec![se_group.clone(), user_group.clone()],
            };
            validate_signature_share_v2(&validate_req)
                .expect("User MPC signature share should validate");
        }
    }

    #[test]
    fn test_v2_different_messages_produce_different_signatures() {
        let (se_kps, se_pub, user_kps, user_pub, combined_vk, tweaked_vk) =
            generate_two_group_keys(3, 2, 3, 2);

        let sig1 = run_v2_sign_and_aggregate(
            &se_kps,
            &se_pub,
            &user_kps,
            &user_pub,
            2,
            2,
            &combined_vk,
            &tweaked_vk,
            b"message one",
        );
        let sig2 = run_v2_sign_and_aggregate(
            &se_kps,
            &se_pub,
            &user_kps,
            &user_pub,
            2,
            2,
            &combined_vk,
            &tweaked_vk,
            b"message two",
        );

        assert_ne!(
            sig1, sig2,
            "Different messages should produce different signatures"
        );
    }

    #[test]
    fn test_v2_sign_frost_missing_fields_errors() {
        let job = FrostSigningJobV2 {
            job_id: "test".to_string(),
            message: b"test".to_vec(),
            key_package: None,
            verifying_key: vec![],
            nonce: None,
            commitments: HashMap::new(),
            participant_groups: vec![],
            adaptor_public_key: vec![],
            use_tweak: false,
        };
        let req = SignFrostRequestV2 {
            signing_jobs: vec![job],
        };
        let result = sign_frost_v2(&req);
        assert!(result.is_err(), "Missing key_package should error");
    }
}
