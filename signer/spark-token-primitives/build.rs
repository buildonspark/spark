use std::{
    io::{Error, ErrorKind, Result},
    path::Path,
};

fn main() -> Result<()> {
    let manifest_dir = std::env::var("CARGO_MANIFEST_DIR").map_err(|err| {
        Error::new(
            ErrorKind::NotFound,
            format!("Failed to get CARGO_MANIFEST_DIR: {err}"),
        )
    })?;
    let manifest_dir = Path::new(&manifest_dir);

    // Prefer vendored protos (present in published crate tarballs), fall back to monorepo path.
    let local_proto_dir = manifest_dir.join("protos");
    let proto_dir = if local_proto_dir.exists() {
        local_proto_dir
    } else {
        manifest_dir.join("../../protos")
    };

    println!(
        "cargo:rerun-if-changed={}",
        proto_dir.join("common.proto").display()
    );
    println!(
        "cargo:rerun-if-changed={}",
        proto_dir.join("spark.proto").display()
    );
    println!(
        "cargo:rerun-if-changed={}",
        proto_dir.join("spark_token.proto").display()
    );
    println!(
        "cargo:rerun-if-changed={}",
        proto_dir.join("multisig.proto").display()
    );

    let protos = &[
        proto_dir.join("common.proto"),
        proto_dir.join("spark.proto"),
        proto_dir.join("spark_token.proto"),
        proto_dir.join("multisig.proto"),
    ];

    prost_build::Config::new().compile_protos(protos, &[proto_dir])?;

    Ok(())
}
