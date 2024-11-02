use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, Mutex};

use frost::*;
use frost_secp256k1_tr::Identifier;
use tonic::{Request, Response, Status};

use frost::frost_service_server::FrostService;
use frost::EchoRequest;
use frost::EchoResponse;

use crate::dkg::DKGState;

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

        let id_bytes: [u8; 32] = hex::decode(req.identifier.clone())
            .map_err(|e| Status::internal(format!("Invalid hex: {:?}", e)))?
            .try_into()
            .map_err(|e| Status::internal(format!("Invalid identifier: {:?}", e)))?;
        let identifier = Identifier::deserialize(&id_bytes)
            .map_err(|e| Status::internal(format!("Failed to deserialize identifier: {:?}", e)))?;
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
        let round1_packages_maps = req
            .round1_packages_maps
            .clone()
            .iter()
            .map(|map| {
                map.packages
                    .iter()
                    .map(|(id, pkg)| {
                        let id_bytes: [u8; 32] =
                            hex::decode(id).expect("Invalid hex").try_into().unwrap();
                        let identifier =
                            Identifier::deserialize(&id_bytes).expect("Invalid identifier");
                        let package =
                            frost_secp256k1_tr::keys::dkg::round1::Package::deserialize(pkg)
                                .expect("Failed to deserialize round1 package");
                        (identifier, package)
                    })
                    .collect::<BTreeMap<_, _>>()
            })
            .collect::<Vec<_>>();

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

        let round1_packages_maps: Vec<BTreeMap<_, _>> = request
            .round1_packages_maps
            .into_iter()
            .map(|package_map| {
                package_map
                    .packages
                    .into_iter()
                    .map(|(identifier, package)| {
                        let id_bytes: [u8; 32] = hex::decode(identifier)
                            .expect("Invalid hex")
                            .try_into()
                            .unwrap();
                        let id = Identifier::deserialize(&id_bytes).expect("Invalid identifier");

                        let package =
                            frost_secp256k1_tr::keys::dkg::round1::Package::deserialize(&package)
                                .expect("Failed to deserialize round1 package");
                        (id, package)
                    })
                    .collect::<BTreeMap<_, _>>()
            })
            .collect();

        let round2_packages_maps: Vec<BTreeMap<_, _>> = request
            .round2_packages_maps
            .into_iter()
            .map(|package_map| {
                package_map
                    .packages
                    .into_iter()
                    .map(|(identifier, package)| {
                        let id_bytes: [u8; 32] = hex::decode(identifier)
                            .expect("Invalid hex")
                            .try_into()
                            .unwrap();
                        let id = Identifier::deserialize(&id_bytes).expect("Invalid identifier");
                        let package =
                            frost_secp256k1_tr::keys::dkg::round2::Package::deserialize(&package)
                                .expect("Failed to deserialize round2 package");
                        (id, package)
                    })
                    .collect::<BTreeMap<_, _>>()
            })
            .collect();

        if round1_packages_maps.len() != round2_secrets.len()
            || round2_packages_maps.len() != round2_secrets.len()
        {
            return Err(Status::internal(
                "Number of packages maps does not match number of round2 secrets",
            ));
        }

        let mut secret_packages = Vec::new();
        let mut public_packages = Vec::new();

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

            secret_packages.push(
                secret_package
                    .serialize()
                    .expect("Failed to serialize secret package"),
            );
            public_packages.push(
                public_package
                    .serialize()
                    .expect("Failed to serialize public package"),
            );
        }

        dkg_state.state = DKGState::None;

        Ok(Response::new(DkgRound3Response {
            secret_packages,
            public_packages,
        }))
    }

    async fn sign_frost(
        &self,
        _request: Request<SignFrostRequest>,
    ) -> Result<Response<SignFrostResponse>, Status> {
        todo!()
    }

    async fn aggregate_frost(
        &self,
        _request: Request<AggregateFrostRequest>,
    ) -> Result<Response<AggregateFrostResponse>, Status> {
        todo!()
    }
    async fn echo(&self, request: Request<EchoRequest>) -> Result<Response<EchoResponse>, Status> {
        let message = request.get_ref().message.clone();
        Ok(Response::new(EchoResponse {
            message: format!("echo: {}", message),
        }))
    }
}
