use std::io::{Error, Result};

fn main() -> Result<()> {
    println!("cargo:rerun-if-changed=src/spark_token_primitives.udl");
    uniffi::generate_scaffolding("./src/spark_token_primitives.udl")
        .map_err(|err| Error::other(err.to_string()))?;

    Ok(())
}
