use std::collections::{BTreeSet, HashMap};
use std::sync::{Arc, Mutex};

use frost::*;
use frost_core::Identifier;
use frost_secp256k1_tr::aggregate_spark;
use tonic::{Request, Response, Status};

use frost::frost_service_server::FrostService;
use frost::EchoRequest;
use frost::EchoResponse;

use crate::dkg::{
    hex_string_to_identifier, key_package_from_dkg_result, round1_package_maps_from_package_maps,
    round2_package_maps_from_package_maps, DKGState,
};
use crate::signing::{
    frost_build_signin_package, frost_commitments_from_proto, frost_key_package_from_proto,
    frost_nonce_from_proto, frost_signature_shares_from_proto,
    frost_signing_commiement_map_from_proto, verifying_key_from_bytes,
};

pub mod frost {
    tonic::include_proto!("frost");
}

#[derive(Debug, Default)]
pub struct FrostDKGState {
    state: DKGState,
}

#[derive(Debug, Default)]
pub struct FrostServer {
    dkg_state: Arc<Mutex<FrostDKGState>>,
}

#[tonic::async_trait]
impl FrostService for FrostServer {
    /// Test function for gRPC connectivity
    ///
    /// This endpoint simply echoes back the received message with a prefix,
    /// allowing clients to verify the gRPC connection is working properly.
    async fn echo(&self, request: Request<EchoRequest>) -> Result<Response<EchoResponse>, Status> {
        let message = request.get_ref().message.clone();
        Ok(Response::new(EchoResponse {
            message: format!("echo: {}", message),
        }))
    }

    async fn dkg_round1(
        &self,
        request: Request<DkgRound1Request>,
    ) -> Result<Response<DkgRound1Response>, Status> {
        let req = request.get_ref();
        if req.min_signers > req.max_signers {
            return Err(Status::invalid_argument(
                "min_signers must be less than max_signers",
            ));
        }

        if req.min_signers < 1 {
            return Err(Status::invalid_argument("min_signers must be at least 1"));
        }

        if req.max_signers > u16::MAX as u64 {
            return Err(Status::invalid_argument(
                "max_signers must be less than 65535",
            ));
        }

        let identifier = hex_string_to_identifier(&req.identifier).map_err(|e| {
            Status::internal(format!(
                "Failed to convert hex string to identifier: {:?}",
                e
            ))
        })?;
        let min_signers = req.min_signers as u16;
        let max_signers = req.max_signers as u16;
        let rng = &mut rand::thread_rng();

        let mut dkg_state = self.dkg_state.lock().unwrap();
        if dkg_state.state != DKGState::None {
            return Err(Status::internal("DKG state is not None"));
        }

        let mut result_secret_packages = Vec::new();
        let mut result_packages = Vec::new();

        for _ in 0..req.key_count {
            let (round1_secret_packages, round1_packages) = frost_secp256k1_tr::keys::dkg::part1(
                identifier,
                max_signers,
                min_signers,
                &mut *rng,
            )
            .map_err(|e| Status::internal(format!("Failed to generate DKG round 1: {:?}", e)))?;
            result_secret_packages.push(round1_secret_packages);
            result_packages.push(round1_packages.serialize().map_err(|e| {
                Status::internal(format!("Failed to serialize DKG round 1 package: {:?}", e))
            })?);
        }

        dkg_state.state = DKGState::Round1(result_secret_packages);

        Ok(Response::new(DkgRound1Response {
            round1_packages: result_packages,
        }))
    }

    async fn dkg_round2(
        &self,
        request: Request<DkgRound2Request>,
    ) -> Result<Response<DkgRound2Response>, Status> {
        let req = request.get_ref();
        let mut dkg_state = self.dkg_state.lock().unwrap();
        let round1_secrets = match &dkg_state.state {
            DKGState::Round1(secrets) => secrets,
            _ => return Err(Status::internal("DKG state is not Round1")),
        };
        let round1_packages_maps = round1_package_maps_from_package_maps(&req.round1_packages_maps)
            .map_err(|e| {
                Status::internal(format!("Failed to parse round1 packages maps: {:?}", e))
            })?;

        if round1_packages_maps.len() != round1_secrets.len() {
            return Err(Status::internal(
                "Number of round1 packages maps does not match number of round1 secrets",
            ));
        }

        let mut result_packages = Vec::new();
        let mut result_secret_packages = Vec::new();
        for (round1_secret, round1_packages_map) in
            round1_secrets.iter().zip(round1_packages_maps.iter())
        {
            let (round2_secret, round2_packages) =
                frost_secp256k1_tr::keys::dkg::part2(round1_secret.clone(), round1_packages_map)
                    .map_err(|e| {
                        Status::internal(format!("Failed to generate DKG round 2: {:?}", e))
                    })?;

            result_secret_packages.push(round2_secret);

            let packages_map = round2_packages
                .into_iter()
                .map(|(id, pkg)| {
                    let serialized = pkg.serialize().expect("Failed to serialize round2 package");
                    (hex::encode(id.serialize()), serialized)
                })
                .collect::<HashMap<String, Vec<u8>>>();

            result_packages.push(PackageMap {
                packages: packages_map,
            });
        }

        dkg_state.state = DKGState::Round2(result_secret_packages);

        Ok(Response::new(DkgRound2Response {
            round2_packages: result_packages,
        }))
    }

    async fn dkg_round3(
        &self,
        request: Request<DkgRound3Request>,
    ) -> Result<Response<DkgRound3Response>, Status> {
        let request = request.into_inner();

        let mut dkg_state = self.dkg_state.lock().unwrap();
        let round2_secrets = match &dkg_state.state {
            DKGState::Round2(secrets) => secrets.clone(),
            _ => {
                return Err(Status::internal(
                    "DKG state is not in Round2, cannot proceed with Round3",
                ));
            }
        };

        let round1_packages_maps =
            round1_package_maps_from_package_maps(&request.round1_packages_maps).map_err(|e| {
                Status::internal(format!("Failed to parse round1 packages maps: {:?}", e))
            })?;

        let round2_packages_maps =
            round2_package_maps_from_package_maps(&request.round2_packages_maps).map_err(|e| {
                Status::internal(format!("Failed to parse round2 packages maps: {:?}", e))
            })?;

        if round1_packages_maps.len() != round2_secrets.len()
            || round2_packages_maps.len() != round2_secrets.len()
        {
            return Err(Status::internal(
                "Number of packages maps does not match number of round2 secrets",
            ));
        }

        let mut key_packages = Vec::new();
        for ((round2_secret, round1_packages), round2_packages) in round2_secrets
            .iter()
            .zip(round1_packages_maps.iter())
            .zip(round2_packages_maps.iter())
        {
            let (secret_package, public_package) = frost_secp256k1_tr::keys::dkg::part3(
                &round2_secret.clone(),
                round1_packages,
                round2_packages,
            )
            .map_err(|e| Status::internal(format!("Failed to generate DKG round 3: {:?}", e)))?;

            let key_package =
                key_package_from_dkg_result(secret_package, public_package).map_err(|e| {
                    Status::internal(format!(
                        "Failed to convert DKG result to key package: {:?}",
                        e
                    ))
                })?;

            key_packages.push(key_package);
        }

        dkg_state.state = DKGState::None;

        Ok(Response::new(DkgRound3Response { key_packages }))
    }

    async fn sign_frost(
        &self,
        request: Request<SignFrostRequest>,
    ) -> Result<Response<SignFrostResponse>, Status> {
        let req = request.get_ref();

        let mut commitments =
            frost_signing_commiement_map_from_proto(&req.commitments).map_err(|e| {
                Status::internal(format!("Failed to parse signing commitments: {:?}", e))
            })?;

        let user_identifier =
            Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

        let signing_participants = match req.role {
            0 => commitments.keys().cloned().collect(),
            1 => BTreeSet::from([user_identifier]),
            _ => return Err(Status::invalid_argument("Invalid signing role")),
        };

        let user_commitments = match &req.user_commitments {
            Some(commitments) => frost_commitments_from_proto(commitments).map_err(|e| {
                Status::internal(format!("Failed to parse user commitments: {:?}", e))
            })?,
            None => return Err(Status::internal("User commitments are required")),
        };
        commitments.insert(user_identifier, user_commitments);

        let nonce = match &req.nonce {
            Some(nonce) => frost_nonce_from_proto(nonce)
                .map_err(|e| Status::internal(format!("Failed to parse nonce: {:?}", e)))?,
            None => return Err(Status::internal("Nonce is required")),
        };

        let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
            .map_err(|e| Status::internal(format!("Failed to parse verifying key: {:?}", e)))?;

        let key_package = match &req.key_package {
            Some(key_package) => frost_key_package_from_proto(key_package)
                .map_err(|e| Status::internal(format!("Failed to parse key package: {:?}", e)))?,
            None => return Err(Status::internal("Key package is required")),
        };

        let signing_package = frost_build_signin_package(commitments, &req.message);
        let signature_share = frost_secp256k1_tr::round2::sign_spark(
            &signing_package,
            &nonce,
            &key_package,
            &signing_participants,
            None,
            None,
            &verifying_key,
        )
        .map_err(|e| Status::internal(format!("Failed to sign frost: {:?}", e)))?;

        Ok(Response::new(SignFrostResponse {
            signature_share: signature_share.serialize().to_vec(),
        }))
    }

    async fn aggregate_frost(
        &self,
        request: Request<AggregateFrostRequest>,
    ) -> Result<Response<AggregateFrostResponse>, Status> {
        let req = request.get_ref();
        let mut commitments =
            frost_signing_commiement_map_from_proto(&req.commitments).map_err(|e| {
                Status::internal(format!("Failed to parse signing commitments: {:?}", e))
            })?;

        let user_identifier =
            Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");

        let user_commitments = match &req.user_commitments {
            Some(commitments) => frost_commitments_from_proto(commitments).map_err(|e| {
                Status::internal(format!("Failed to parse user commitments: {:?}", e))
            })?,
            None => return Err(Status::internal("User commitments are required")),
        };
        commitments.insert(user_identifier, user_commitments);

        let verifying_key = verifying_key_from_bytes(req.verifying_key.clone())
            .map_err(|e| Status::internal(format!("Failed to parse verifying key: {:?}", e)))?;

        let signing_package = frost_build_signin_package(commitments, &req.message);

        let signature_shares = frost_signature_shares_from_proto(&req.signature_shares)
            .map_err(|e| Status::internal(format!("Failed to parse signature shares: {:?}", e)))?;

        let signature = aggregate_spark(&signing_package, &signature_shares, &verifying_key)
            .map_err(|e| Status::internal(format!("Failed to aggregate frost: {:?}", e)))?;

        Ok(Response::new(AggregateFrostResponse {
            signature: signature.serialize().to_vec(),
        }))
    }
}
