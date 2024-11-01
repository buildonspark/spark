use std::fs;

use server::{frost::frost_service_server, FrostServer};
use tokio::net::UnixListener;
use tonic::transport::Server;

mod server;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let sock_path = "/tmp/frost.sock";
    let _ = fs::remove_file(sock_path);

    let uds = UnixListener::bind(sock_path)?;
    let uds_stream = tokio_stream::wrappers::UnixListenerStream::new(uds);

    let frost_server = FrostServer::default();
    Server::builder()
        .add_service(frost_service_server::FrostServiceServer::new(frost_server))
        .serve_with_incoming(uds_stream)
        .await?;
    Ok(())
}
