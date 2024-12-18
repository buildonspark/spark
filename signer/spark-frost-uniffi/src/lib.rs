uniffi::include_scaffolding!("spark_frost");

use std::collections::HashMap;

use frost_secp256k1_tr::Identifier;

/// A uniffi library for the Spark Frost signing protocol on client side.
/// This only signs as the required participant in the signing protocol.
///
#[derive(Debug, Clone)]
pub enum Error {
    Frost(String),
}

impl From<String> for Error {
    fn from(s: String) -> Self {
        Error::Frost(s)
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self)
    }
}

#[derive(Debug, Clone)]
pub struct SigningNonce {
    pub hiding: Vec<u8>,
    pub binding: Vec<u8>,
}

impl Into<spark_frost::proto::frost::SigningNonce> for SigningNonce {
    fn into(self) -> spark_frost::proto::frost::SigningNonce {
        spark_frost::proto::frost::SigningNonce {
            hiding: self.hiding,
            binding: self.binding,
        }
    }
}

impl From<spark_frost::proto::frost::SigningNonce> for SigningNonce {
    fn from(proto: spark_frost::proto::frost::SigningNonce) -> Self {
        SigningNonce {
            hiding: proto.hiding,
            binding: proto.binding,
        }
    }
}

#[derive(Debug, Clone)]
pub struct SigningCommitment {
    pub hiding: Vec<u8>,
    pub binding: Vec<u8>,
}

impl Into<spark_frost::proto::common::SigningCommitment> for SigningCommitment {
    fn into(self) -> spark_frost::proto::common::SigningCommitment {
        spark_frost::proto::common::SigningCommitment {
            hiding: self.hiding,
            binding: self.binding,
        }
    }
}

impl From<spark_frost::proto::common::SigningCommitment> for SigningCommitment {
    fn from(proto: spark_frost::proto::common::SigningCommitment) -> Self {
        SigningCommitment {
            hiding: proto.hiding,
            binding: proto.binding,
        }
    }
}

#[derive(Debug, Clone)]
pub struct NonceResult {
    pub nonce: SigningNonce,
    pub commitment: SigningCommitment,
}

impl From<spark_frost::proto::frost::SigningNonceResult> for NonceResult {
    fn from(proto: spark_frost::proto::frost::SigningNonceResult) -> Self {
        NonceResult {
            nonce: proto.nonces.clone().expect("No nonce").into(),
            commitment: proto.commitments.clone().expect("No commitment").into(),
        }
    }
}

#[derive(Debug, Clone)]
pub struct KeyPackage {
    // The secret key for the user.
    pub secret_key: Vec<u8>,
    // The public key for the user.
    pub public_key: Vec<u8>,
    // The verifying key for the user + SE.
    pub verifying_key: Vec<u8>,
}

impl Into<spark_frost::proto::frost::KeyPackage> for KeyPackage {
    fn into(self) -> spark_frost::proto::frost::KeyPackage {
        let user_identifier =
            Identifier::derive("user".as_bytes()).expect("Failed to derive user identifier");
        let user_identifier_string = hex::encode(user_identifier.to_scalar().to_bytes());
        spark_frost::proto::frost::KeyPackage {
            identifier: user_identifier_string.clone(),
            secret_share: self.secret_key.clone(),
            public_shares: HashMap::from([(
                user_identifier_string.clone(),
                self.public_key.clone(),
            )]),
            public_key: self.verifying_key.clone(),
            min_signers: 1,
        }
    }
}

pub fn frost_nonce(key_package: KeyPackage) -> Result<NonceResult, Error> {
    let key_package_proto: spark_frost::proto::frost::KeyPackage = key_package.into();
    let request = spark_frost::proto::frost::FrostNonceRequest {
        key_packages: vec![key_package_proto],
    };
    let response = spark_frost::signing::frost_nonce(&request).map_err(|e| Error::Frost(e))?;
    let nonce = response
        .results
        .first()
        .ok_or(Error::Frost("No nonce generated".to_owned()))?
        .clone();
    Ok(nonce.into())
}

pub fn sign_frost(
    msg: Vec<u8>,
    key_package: KeyPackage,
    nonce: SigningNonce,
    self_commitment: SigningCommitment,
    statechain_commitments: HashMap<String, SigningCommitment>,
) -> Result<Vec<u8>, Error> {
    let signing_job = spark_frost::proto::frost::FrostSigningJob {
        job_id: uuid::Uuid::new_v4().to_string(),
        message: msg,
        key_package: Some(key_package.clone().into()),
        nonce: Some(nonce.into()),
        user_commitments: Some(self_commitment.into()),
        verifying_key: key_package.clone().verifying_key.clone(),
        commitments: statechain_commitments
            .into_iter()
            .map(|(k, v)| (k, v.into()))
            .collect(),
    };
    let request = spark_frost::proto::frost::SignFrostRequest {
        signing_jobs: vec![signing_job],
        role: spark_frost::proto::frost::SigningRole::User.into(),
    };
    let response = spark_frost::signing::sign_frost(&request).map_err(|e| Error::Frost(e))?;
    let result = response
        .results
        .iter()
        .next()
        .ok_or(Error::Frost("No result".to_owned()))?
        .1;
    Ok(result.signature_share.clone())
}

pub fn aggregate_frost(
    msg: Vec<u8>,
    statechain_commitments: HashMap<String, SigningCommitment>,
    self_commitment: SigningCommitment,
    statechain_signatures: HashMap<String, Vec<u8>>,
    self_signature: Vec<u8>,
    statechain_public_keys: HashMap<String, Vec<u8>>,
    self_public_key: Vec<u8>,
    verifying_key: Vec<u8>,
) -> Result<Vec<u8>, Error> {
    let request = spark_frost::proto::frost::AggregateFrostRequest {
        message: msg,
        commitments: statechain_commitments
            .into_iter()
            .map(|(k, v)| (k, v.into()))
            .collect(),
        user_commitments: Some(self_commitment.into()),
        user_public_key: self_public_key.clone(),
        signature_shares: statechain_signatures
            .into_iter()
            .map(|(k, v)| (k, v.clone()))
            .collect(),
        public_shares: statechain_public_keys
            .into_iter()
            .map(|(k, v)| (k, v.clone()))
            .collect(),
        verifying_key: verifying_key.clone(),
        user_signature_share: self_signature.clone(),
    };
    let response = spark_frost::signing::aggregate_frost(&request).map_err(|e| Error::Frost(e))?;
    Ok(response.signature)
}
