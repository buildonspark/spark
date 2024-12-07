use std::fs;

use clap::{arg, Parser};
use server::FrostServer;
use spark_frost::proto::frost::frost_service_server;
use tokio::net::{TcpListener, UnixListener};
use tonic::transport::Server;
use tracing::Level;

mod dkg;
mod server;

#[derive(Parser)]
#[command(version, about, long_about = None)]
struct Args {
    /// TCP port to listen on (e.g., 8080)
    #[arg(short, long, group = "listen_on", value_parser = clap::value_parser!(u16).range(1..))]
    port: Option<u16>,

    /// Unix domain socket path to listen on
    #[arg(short, long, group = "listen_on")]
    unix: Option<String>,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args = Args::parse();

    tracing_subscriber::fmt()
        .with_target(false)
        .with_thread_ids(true)
        .with_level(true)
        .with_file(true)
        .with_line_number(true)
        .with_thread_names(true)
        .with_max_level(Level::DEBUG)
        .init();

    let frost_server = FrostServer::default();

    match (args.port, args.unix) {
        (Some(port), None) => {
            tracing::info!("Listening on port {}", port);
            let listener = TcpListener::bind(("0.0.0.0", port)).await?;
            let stream = tokio_stream::wrappers::TcpListenerStream::new(listener);
            Server::builder()
                .add_service(frost_service_server::FrostServiceServer::new(frost_server))
                .serve_with_incoming(stream)
                .await?;
        }
        (None, Some(unix)) => {
            tracing::info!("Listening on unix socket {}", unix);
            let _ = fs::remove_file(unix.clone());
            let listener = UnixListener::bind(unix.clone())?;
            let stream = tokio_stream::wrappers::UnixListenerStream::new(listener);
            Server::builder()
                .add_service(frost_service_server::FrostServiceServer::new(frost_server))
                .serve_with_incoming(stream)
                .await?;
        }
        _ => return Err("Invalid listen options".into()),
    };

    Ok(())
}
