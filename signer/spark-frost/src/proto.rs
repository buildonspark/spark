#[allow(clippy::module_inception)]
#[cfg(target_arch = "wasm32")]
pub mod common {
    include!(concat!(env!("OUT_DIR"), "/common.rs"));
}

#[allow(clippy::module_inception)]
#[cfg(target_arch = "wasm32")]
pub mod frost {
    include!(concat!(env!("OUT_DIR"), "/frost.rs"));
}

#[allow(clippy::module_inception)]
#[cfg(not(target_arch = "wasm32"))]
pub mod common {
    tonic::include_proto!("common");
}

#[allow(clippy::module_inception)]
#[cfg(not(target_arch = "wasm32"))]
pub mod frost {
    tonic::include_proto!("frost");
}
