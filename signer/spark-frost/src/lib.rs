use frost_secp256k1_tr::Identifier;

mod frost_test;
pub mod proto;
pub mod signing;

/// Convert a hex string to an identifier.
pub fn hex_string_to_identifier(identifier: &str) -> Result<Identifier, String> {
    let id_bytes: [u8; 32] = hex::decode(identifier)
        .map_err(|e| format!("Invalid hex: {:?}", e))?
        .try_into()
        .map_err(|e| format!("Identifier is not 32 bytes: {:?}", e))?;
    Identifier::deserialize(&id_bytes)
        .map_err(|e| format!("Failed to deserialize identifier: {:?}", e))
}
