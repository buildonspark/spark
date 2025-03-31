use std::{
    io::{Error, ErrorKind, Result},
    path::Path,
};

fn main() -> Result<()> {
    let build_dir_env = std::env::var("CARGO_MANIFEST_DIR").map_err(|err| {
        Error::new(
            ErrorKind::NotFound,
            format!("Failed to get CARGO_MANIFEST_DIR: {}", err),
        )
    })?;
    let build_dir = Path::new(&build_dir_env);

    let proto_dir = build_dir.join("../../../protos/");
    let protos = &[
        proto_dir.join("spark.proto"),
        proto_dir.join("spark_authn.proto"),
    ];

    tonic_build::configure()
        .build_client(true)
        .build_server(false)
        .compile_protos(protos, &[proto_dir])?;

    Ok(())
}
