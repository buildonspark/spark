#[allow(clippy::large_enum_variant)]
pub mod common {
    include!(concat!(env!("OUT_DIR"), "/common.rs"));
}

#[allow(clippy::large_enum_variant)]
pub mod multisig {
    include!(concat!(env!("OUT_DIR"), "/multisig.rs"));
}

#[allow(clippy::large_enum_variant)]
pub mod spark {
    include!(concat!(env!("OUT_DIR"), "/spark.rs"));
}

#[allow(clippy::large_enum_variant)]
pub mod spark_token {
    include!(concat!(env!("OUT_DIR"), "/spark_token.rs"));
}
