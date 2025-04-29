pub mod common {
    include!(concat!(env!("OUT_DIR"), "/common.rs"));
}

pub mod spark {
    include!(concat!(env!("OUT_DIR"), "/spark.rs"));
}

pub mod spark_authn {
    include!(concat!(env!("OUT_DIR"), "/spark_authn.rs"));
}
