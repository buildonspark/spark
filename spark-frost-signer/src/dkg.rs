#[derive(Debug, Default, PartialEq)]
pub enum DKGState {
    #[default]
    None,
    Round1(Vec<frost_secp256k1_tr::keys::dkg::round1::SecretPackage>),
    Round2(Vec<frost_secp256k1_tr::keys::dkg::round2::SecretPackage>),
}
