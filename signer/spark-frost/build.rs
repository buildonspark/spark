fn main() -> Result<(), Box<dyn std::error::Error>> {
    let target_arch = std::env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default();
    if target_arch == "wasm32" {
        prost_build::Config::new()
            .compile_protos(&["protos/common.proto", "protos/frost.proto"], &["protos"])
            .unwrap();
    } else {
        tonic_build::configure()
            .compile_protos(&["protos/common.proto", "protos/frost.proto"], &["protos"])?;
    }
    Ok(())
}
