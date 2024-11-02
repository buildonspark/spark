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
        if req.identifier == 0 || req.identifier > u16::MAX as u64 {
            return Err(Status::invalid_argument(
                "identifier must be between 1 and 65535",
            ));
        }

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

        let identifier = Identifier::try_from(req.identifier as u16).expect("Invalid identifier");
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
        _request: Request<DkgRound2Request>,
    ) -> Result<Response<DkgRound2Response>, Status> {
        todo!()
    }

    async fn dkg_round3(
        &self,
        _request: Request<DkgRound3Request>,
    ) -> Result<Response<DkgRound3Response>, Status> {
        todo!()
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
