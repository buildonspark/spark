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

    let proto_dir = build_dir.join("../../protos/");
    let protos = &[
        proto_dir.join("common.proto"),
        proto_dir.join("frost.proto"),
    ];

    let target_arch = std::env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default();
    if target_arch == "wasm32" {
        prost_build::Config::new()
            .compile_protos(protos, &[proto_dir])
            .unwrap();
    } else {
        tonic_build::configure().compile_protos(protos, &[proto_dir])?;
    }

    Ok(())
}
