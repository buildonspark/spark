use tonic::{Request, Response, Status};

use frost::frost_service_server::FrostService;
use frost::EchoRequest;
use frost::EchoResponse;

pub mod frost {
    tonic::include_proto!("frost");
}

#[derive(Debug, Default)]
pub struct FrostServer {}

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
}
