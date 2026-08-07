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

    tracing::debug!("signing_package: {:?}", signing_package);
    tracing::debug!("signature_shares: {:?}", signature_shares);
    tracing::debug!("public_package: {:?}", public_package);

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

/// Upper bound on jobs per aggregate_frost_batch request. Bounds both the
/// per-request message size (the signer keeps tonic's default 4 MiB decode
/// limit) and how much of the shared rayon pool a single request can occupy.
/// Callers chunk larger batches into multiple requests.
pub const MAX_AGGREGATE_BATCH_JOBS: usize = 100;

/// Failures from aggregate_frost_batch, split by blame so the gRPC layer can
/// report malformed batches as INVALID_ARGUMENT instead of INTERNAL.
#[derive(Debug, PartialEq, Eq)]
pub enum BatchAggregateError {
    InvalidArgument(String),
    Internal(String),
}

pub fn aggregate_frost_batch(
    req: &AggregateFrostBatchRequest,
) -> Result<AggregateFrostBatchResponse, BatchAggregateError> {
    if req.jobs.len() > MAX_AGGREGATE_BATCH_JOBS {
        return Err(BatchAggregateError::InvalidArgument(format!(
            "Aggregate batch has {} jobs, exceeding the limit of {MAX_AGGREGATE_BATCH_JOBS}",
            req.jobs.len()
        )));
    }
    let mut seen_job_ids = std::collections::HashSet::with_capacity(req.jobs.len());
    for job in &req.jobs {
        if job.job_id.is_empty() {
            return Err(BatchAggregateError::InvalidArgument(
                "Aggregate job with empty job id".to_string(),
            ));
        }
        if !seen_job_ids.insert(job.job_id.as_str()) {
            return Err(BatchAggregateError::InvalidArgument(format!(
                "Duplicate job id in aggregate batch: {}",
                job.job_id
            )));
        }
        if job.request.is_none() {
            return Err(BatchAggregateError::InvalidArgument(format!(
                "Aggregate job {} is missing a request",
                job.job_id
            )));
        }
    }

    let results: HashMap<String, AggregateFrostResponse> = req
        .jobs
        .par_iter()
        .map(|job| {
            // Presence is validated above; treat absence as internal.
            let request = job
                .request
                .as_ref()
                .ok_or_else(|| format!("Aggregate job {} is missing a request", job.job_id))?;
            let response = aggregate_frost(request)
                .map_err(|e| format!("Failed to aggregate frost for job {}: {e}", job.job_id))?;
            Ok((job.job_id.clone(), response))
        })
        .collect::<Result<HashMap<String, AggregateFrostResponse>, String>>()
        .map_err(BatchAggregateError::Internal)?;

    Ok(AggregateFrostBatchResponse { results })
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
        keys::{KeyPackage as FrostKP, PublicKeyPackage, Tweak},
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

    /// Key material for the LEGACY (v1) sign + aggregate flow: an SE dealer
    /// group plus a single user key under the derived "user" identifier.
    /// Legacy SE participants sign with raw dealer shares and
    /// verifying_key = combined (untweaked) key; sign_with_tweak applies
    /// even-Y and the taproot tweak internally. The user key package is
    /// even-Y adjusted by frost_key_package_from_proto (role = USER).
    struct LegacyKeys {
        se_key_packages: BTreeMap<Identifier, FrostKP>,
        user_key: frost_secp256k1_tr::SigningKey,
        user_vk: VerifyingKey,
        combined_vk: VerifyingKey,
        se_min: u16,
    }

    fn generate_legacy_keys(se_max: u16, se_min: u16) -> LegacyKeys {
        let mut rng = thread_rng();
        let (se_shares, se_pubkey_pkg) = frost::keys::generate_with_dealer(
            se_max,
            se_min,
            frost::keys::IdentifierList::Default,
            &mut rng,
        )
        .unwrap();
        let se_key_packages: BTreeMap<Identifier, FrostKP> = se_shares
            .into_iter()
            .map(|(id, ss)| (id, FrostKP::try_from(ss).unwrap()))
            .collect();

        let user_key = frost_secp256k1_tr::SigningKey::new(&mut rng);
        let user_vk = VerifyingKey::from(&user_key);
        let combined_vk =
            VerifyingKey::new(se_pubkey_pkg.verifying_key().to_element() + user_vk.to_element());

        LegacyKeys {
            se_key_packages,
            user_key,
            user_vk,
            combined_vk,
            se_min,
        }
    }

    /// Run one full legacy round-1 + round-2 signing pass over `message` and
    /// return the AggregateFrostRequest that a coordinator would send.
    fn build_legacy_aggregate_request(keys: &LegacyKeys, message: &[u8]) -> AggregateFrostRequest {
        let mut rng = thread_rng();
        let user_identifier = Identifier::derive(b"user").unwrap();

        let se_signers: Vec<Identifier> = keys
            .se_key_packages
            .keys()
            .take(keys.se_min as usize)
            .cloned()
            .collect();

        // Round 1: nonces + commitments for the SE signers and the user.
        let mut nonces_map: BTreeMap<Identifier, frost_secp256k1_tr::round1::SigningNonces> =
            BTreeMap::new();
        let mut se_proto_commitments: HashMap<String, SigningCommitment> = HashMap::new();
        for id in &se_signers {
            let kp = &keys.se_key_packages[id];
            let (nonce, commitment) = frost::round1::commit(kp.signing_share(), &mut rng);
            nonces_map.insert(*id, nonce);
            se_proto_commitments.insert(id_to_hex(id), commitment_to_proto(&commitment));
        }
        let user_signing_share =
            frost_secp256k1_tr::keys::SigningShare::new(keys.user_key.clone().to_scalar());
        let (user_nonce, user_commitment) = frost::round1::commit(&user_signing_share, &mut rng);
        let user_proto_commitment = commitment_to_proto(&user_commitment);

        let se_public_shares: HashMap<String, Vec<u8>> = se_signers
            .iter()
            .map(|id| {
                let kp = &keys.se_key_packages[id];
                (
                    id_to_hex(id),
                    kp.verifying_share().serialize().unwrap().to_vec(),
                )
            })
            .collect();

        // Round 2: each SE signer signs with role STATECHAIN.
        let mut se_signature_shares: HashMap<String, Vec<u8>> = HashMap::new();
        for id in &se_signers {
            let kp = &keys.se_key_packages[id];
            let nonce = &nonces_map[id];
            let proto_kp = KeyPackage {
                identifier: id_to_hex(id),
                secret_share: kp.signing_share().serialize().to_vec(),
                public_shares: se_public_shares.clone(),
                public_key: keys.combined_vk.serialize().unwrap().to_vec(),
                min_signers: keys.se_min as u32,
            };
            let job = FrostSigningJob {
                job_id: id_to_hex(id),
                message: message.to_vec(),
                key_package: Some(proto_kp),
                verifying_key: keys.combined_vk.serialize().unwrap().to_vec(),
                nonce: Some(SigningNonce {
                    hiding: nonce.hiding().serialize().to_vec(),
                    binding: nonce.binding().serialize().to_vec(),
                }),
                commitments: se_proto_commitments.clone(),
                user_commitments: Some(user_proto_commitment.clone()),
                adaptor_public_key: vec![],
            };
            let resp = sign_frost(&SignFrostRequest {
                signing_jobs: vec![job],
                role: 0,
            })
            .unwrap();
            let share = resp.results.get(&id_to_hex(id)).unwrap();
            se_signature_shares.insert(id_to_hex(id), share.signature_share.clone());
        }

        // Round 2: the user signs with role USER.
        let user_verifying_share =
            frost_secp256k1_tr::keys::VerifyingShare::new(keys.user_vk.to_element());
        let user_proto_kp = KeyPackage {
            identifier: id_to_hex(&user_identifier),
            secret_share: user_signing_share.serialize().to_vec(),
            public_shares: HashMap::from([(
                id_to_hex(&user_identifier),
                user_verifying_share.serialize().unwrap().to_vec(),
            )]),
            public_key: keys.user_vk.serialize().unwrap().to_vec(),
            min_signers: 1,
        };
        let user_job = FrostSigningJob {
            job_id: "user".to_string(),
            message: message.to_vec(),
            key_package: Some(user_proto_kp),
            verifying_key: keys.combined_vk.serialize().unwrap().to_vec(),
            nonce: Some(SigningNonce {
                hiding: user_nonce.hiding().serialize().to_vec(),
                binding: user_nonce.binding().serialize().to_vec(),
            }),
            commitments: se_proto_commitments.clone(),
            user_commitments: Some(user_proto_commitment.clone()),
            adaptor_public_key: vec![],
        };
        let user_resp = sign_frost(&SignFrostRequest {
            signing_jobs: vec![user_job],
            role: 1,
        })
        .unwrap();
        let user_signature_share = user_resp
            .results
            .get("user")
            .unwrap()
            .signature_share
            .clone();

        AggregateFrostRequest {
            message: message.to_vec(),
            signature_shares: se_signature_shares,
            public_shares: se_public_shares,
            verifying_key: keys.combined_vk.serialize().unwrap().to_vec(),
            commitments: se_proto_commitments,
            user_commitments: Some(user_proto_commitment),
            user_public_key: keys.user_vk.serialize().unwrap().to_vec(),
            user_signature_share,
            adaptor_public_key: vec![],
        }
    }

    fn verify_legacy_signature(keys: &LegacyKeys, message: &[u8], sig: &[u8]) {
        let merkle_root = vec![];
        let mut all_shares: BTreeMap<Identifier, frost_secp256k1_tr::keys::VerifyingShare> =
            BTreeMap::new();
        for (id, kp) in &keys.se_key_packages {
            all_shares.insert(*id, *kp.verifying_share());
        }
        let combined_pkg = PublicKeyPackage::new(all_shares, keys.combined_vk, None);
        let tweaked_pkg = combined_pkg.tweak(Some(&merkle_root));
        let frost_sig = frost_secp256k1_tr::Signature::deserialize(sig).unwrap();
        tweaked_pkg
            .verifying_key()
            .verify(message, &frost_sig)
            .expect("legacy signature should verify against tweaked VK");
    }

    #[test]
    fn test_legacy_sign_and_aggregate() {
        let keys = generate_legacy_keys(5, 3);
        let message = b"legacy single aggregate";
        let req = build_legacy_aggregate_request(&keys, message);
        let resp = aggregate_frost(&req).unwrap();
        verify_legacy_signature(&keys, message, &resp.signature);
    }

    #[test]
    fn test_aggregate_frost_batch_matches_single() {
        let keys = generate_legacy_keys(3, 2);
        let messages: Vec<Vec<u8>> = (0..8)
            .map(|i| format!("batch aggregate message {i}").into_bytes())
            .collect();
        let requests: Vec<AggregateFrostRequest> = messages
            .iter()
            .map(|m| build_legacy_aggregate_request(&keys, m))
            .collect();

        let batch_req = AggregateFrostBatchRequest {
            jobs: requests
                .iter()
                .enumerate()
                .map(|(i, r)| AggregateFrostJob {
                    job_id: format!("job-{i}"),
                    request: Some(r.clone()),
                })
                .collect(),
        };
        let batch_resp = aggregate_frost_batch(&batch_req).unwrap();
        assert_eq!(batch_resp.results.len(), requests.len());

        for (i, (message, request)) in messages.iter().zip(&requests).enumerate() {
            let single = aggregate_frost(request).unwrap();
            let batched = batch_resp.results.get(&format!("job-{i}")).unwrap();
            assert_eq!(
                batched.signature, single.signature,
                "batch and single aggregation should produce identical signatures"
            );
            verify_legacy_signature(&keys, message, &batched.signature);
        }
    }

    #[test]
    fn test_aggregate_frost_batch_empty() {
        let resp = aggregate_frost_batch(&AggregateFrostBatchRequest { jobs: vec![] }).unwrap();
        assert!(resp.results.is_empty());
    }

    fn expect_invalid_argument(err: BatchAggregateError, expected_fragment: &str) {
        match err {
            BatchAggregateError::InvalidArgument(msg) => assert!(
                msg.contains(expected_fragment),
                "unexpected error message: {msg}"
            ),
            BatchAggregateError::Internal(msg) => {
                panic!("expected InvalidArgument, got Internal: {msg}")
            }
        }
    }

    #[test]
    fn test_aggregate_frost_batch_duplicate_job_ids_error() {
        let keys = generate_legacy_keys(3, 2);
        let request = build_legacy_aggregate_request(&keys, b"dup");
        let batch_req = AggregateFrostBatchRequest {
            jobs: vec![
                AggregateFrostJob {
                    job_id: "same".to_string(),
                    request: Some(request.clone()),
                },
                AggregateFrostJob {
                    job_id: "same".to_string(),
                    request: Some(request),
                },
            ],
        };
        let err = aggregate_frost_batch(&batch_req).unwrap_err();
        expect_invalid_argument(err, "Duplicate job id");
    }

    #[test]
    fn test_aggregate_frost_batch_empty_job_id_errors() {
        let keys = generate_legacy_keys(3, 2);
        let request = build_legacy_aggregate_request(&keys, b"empty-id");
        let batch_req = AggregateFrostBatchRequest {
            jobs: vec![AggregateFrostJob {
                job_id: String::new(),
                request: Some(request),
            }],
        };
        let err = aggregate_frost_batch(&batch_req).unwrap_err();
        expect_invalid_argument(err, "empty job id");
    }

    #[test]
    fn test_aggregate_frost_batch_too_many_jobs_errors() {
        let jobs = (0..=MAX_AGGREGATE_BATCH_JOBS)
            .map(|i| AggregateFrostJob {
                job_id: format!("job-{i}"),
                request: Some(AggregateFrostRequest::default()),
            })
            .collect();
        let err = aggregate_frost_batch(&AggregateFrostBatchRequest { jobs }).unwrap_err();
        expect_invalid_argument(err, "exceeding the limit");
    }

    #[test]
    fn test_aggregate_frost_batch_missing_request_errors() {
        let batch_req = AggregateFrostBatchRequest {
            jobs: vec![AggregateFrostJob {
                job_id: "no-request".to_string(),
                request: None,
            }],
        };
        let err = aggregate_frost_batch(&batch_req).unwrap_err();
        expect_invalid_argument(err, "no-request");
    }

    #[test]
    fn test_aggregate_frost_batch_bad_job_fails_batch_with_job_id() {
        let keys = generate_legacy_keys(3, 2);
        let good = build_legacy_aggregate_request(&keys, b"good");
        let mut bad = build_legacy_aggregate_request(&keys, b"bad");
        bad.user_signature_share = vec![0xff; 3];

        let batch_req = AggregateFrostBatchRequest {
            jobs: vec![
                AggregateFrostJob {
                    job_id: "good-job".to_string(),
                    request: Some(good),
                },
                AggregateFrostJob {
                    job_id: "bad-job".to_string(),
                    request: Some(bad),
                },
            ],
        };
        match aggregate_frost_batch(&batch_req).unwrap_err() {
            BatchAggregateError::Internal(msg) => {
                assert!(msg.contains("bad-job"), "unexpected error message: {msg}")
            }
            BatchAggregateError::InvalidArgument(msg) => {
                panic!("expected Internal, got InvalidArgument: {msg}")
            }
        }
    }
}
