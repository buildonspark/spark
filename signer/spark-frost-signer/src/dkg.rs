use std::collections::BTreeMap;

use frost_secp256k1_tr::{
    keys::{KeyPackage as FrostKeyPackage, PublicKeyPackage},
    Identifier,
};

use spark_frost::proto::{common::PackageMap, frost::KeyPackage};

#[derive(Debug, PartialEq)]
pub enum DKGState {
    Round1(Vec<frost_secp256k1_tr::keys::dkg::round1::SecretPackage>),
    Round2(Vec<frost_secp256k1_tr::keys::dkg::round2::SecretPackage>),
}

/// Convert a package map to a map of identifiers to round1 packages.
pub fn round1_package_map_from_package_map(
    package_map: &PackageMap,
) -> Result<BTreeMap<Identifier, frost_secp256k1_tr::keys::dkg::round1::Package>, String> {
    let mut result = BTreeMap::new();
    for (id, package) in package_map.packages.iter() {
        let identifier = spark_frost::hex_string_to_identifier(id)?;
        let package = frost_secp256k1_tr::keys::dkg::round1::Package::deserialize(package)
            .map_err(|e| format!("Failed to deserialize round1 package: {:?}", e))?;
        result.insert(identifier, package);
    }
    Ok(result)
}

/// Convert a vector of package maps to a vector of maps of identifiers to round1 packages.
pub fn round1_package_maps_from_package_maps(
    package_maps: &[PackageMap],
) -> Result<Vec<BTreeMap<Identifier, frost_secp256k1_tr::keys::dkg::round1::Package>>, String> {
    package_maps
        .iter()
        .map(round1_package_map_from_package_map)
        .collect()
}

/// Convert a package map to a map of identifiers to round2 packages.
pub fn round2_package_map_from_package_map(
    package_map: &PackageMap,
) -> Result<BTreeMap<Identifier, frost_secp256k1_tr::keys::dkg::round2::Package>, String> {
    let mut result = BTreeMap::new();
    for (id, package) in package_map.packages.iter() {
        let identifier = spark_frost::hex_string_to_identifier(id)?;
        let package = frost_secp256k1_tr::keys::dkg::round2::Package::deserialize(package)
            .map_err(|e| format!("Failed to deserialize round2 package: {:?}", e))?;
        result.insert(identifier, package);
    }
    Ok(result)
}

/// Convert a vector of package maps to a vector of maps of identifiers to round2 packages.
pub fn round2_package_maps_from_package_maps(
    package_maps: &[PackageMap],
) -> Result<Vec<BTreeMap<Identifier, frost_secp256k1_tr::keys::dkg::round2::Package>>, String> {
    package_maps
        .iter()
        .map(round2_package_map_from_package_map)
        .collect()
}

pub fn key_package_from_dkg_result(
    secret_package: FrostKeyPackage,
    public_package: PublicKeyPackage,
) -> Result<KeyPackage, String> {
    let secret_share = secret_package.signing_share().serialize();
    let identifier = hex::encode(secret_package.identifier().serialize());

    let public_shares = public_package
        .verifying_shares()
        .iter()
        .map(|(id, share)| {
            (
                hex::encode(id.serialize()),
                share.serialize().expect("failed to serialize share"),
            )
        })
        .collect();

    Ok(KeyPackage {
        secret_share: secret_share.to_vec(),
        identifier,
        min_signers: *secret_package.min_signers() as u32,
        public_key: public_package
            .verifying_key()
            .serialize()
            .expect("failed to serialize public key"),
        public_shares,
    })
}
