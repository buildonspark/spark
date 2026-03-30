use std::{
    io::{Error, ErrorKind, Result},
    path::Path,
};

fn main() -> Result<()> {
    println!("cargo:rerun-if-changed=../../protos/common.proto");
    println!("cargo:rerun-if-changed=../../protos/spark.proto");
    println!("cargo:rerun-if-changed=../../protos/spark_token.proto");
    println!("cargo:rerun-if-changed=../../protos/multisig.proto");

    let manifest_dir = std::env::var("CARGO_MANIFEST_DIR").map_err(|err| {
        Error::new(
            ErrorKind::NotFound,
            format!("Failed to get CARGO_MANIFEST_DIR: {err}"),
        )
    })?;
    let manifest_dir = Path::new(&manifest_dir);
    let proto_dir = manifest_dir.join("../../protos");
    let protos = &[
        proto_dir.join("common.proto"),
        proto_dir.join("spark.proto"),
        proto_dir.join("spark_token.proto"),
        proto_dir.join("multisig.proto"),
    ];

    prost_build::Config::new().compile_protos(protos, &[proto_dir])?;

    Ok(())
}
