//! BIP-340-style tagged hashing, mirroring `spark/common/hashstructure/hasher.go`
//! (Go) and `sdks/js/packages/spark-sdk/src/utils/hashstructure.ts` (JS).
//!
//! Only the subset the transfer-manifest digest needs is ported; add the other
//! `Add*` variants from the Go reference if a new caller requires them.
//!
//! Construction:
//!
//!   tagHash = SHA256(serializeTag(tag))
//!   result  = SHA256(tagHash || tagHash || values...)
//!
//! where each tag component and each value is serialized as
//! `[8-byte big-endian length][bytes]`.

use sha2::{Digest, Sha256};

pub(crate) struct Hasher {
    hasher: Sha256,
}

impl Hasher {
    pub(crate) fn new(tag: &[&str]) -> Self {
        let mut tag_bytes = Vec::new();
        for component in tag {
            tag_bytes.extend_from_slice(&(component.len() as u64).to_be_bytes());
            tag_bytes.extend_from_slice(component.as_bytes());
        }
        let tag_hash = Sha256::digest(&tag_bytes);

        let mut hasher = Sha256::new();
        hasher.update(tag_hash);
        hasher.update(tag_hash);
        Self { hasher }
    }

    pub(crate) fn add_bytes(mut self, value: &[u8]) -> Self {
        self.hasher.update((value.len() as u64).to_be_bytes());
        self.hasher.update(value);
        self
    }

    pub(crate) fn hash(self) -> Vec<u8> {
        self.hasher.finalize().to_vec()
    }
}

#[cfg(test)]
mod tests {
    use super::Hasher;

    fn hex(bytes: &[u8]) -> String {
        bytes.iter().map(|b| format!("{b:02x}")).collect()
    }

    // Pins computed independently from the Go hashstructure definition, so this
    // port is verified against the algorithm itself rather than only
    // transitively through the manifest golden vectors.
    #[test]
    fn matches_go_hashstructure_construction() {
        let got = Hasher::new(&["spark", "transfer", "manifest"])
            .add_bytes(&[0u8; 32])
            .hash();
        assert_eq!(
            hex(&got),
            "4d18d5d3166ebfde902314ab397790413893c736b41e18d72cd726d707de6c42"
        );

        let empty = Hasher::new(&[]).hash();
        assert_eq!(
            hex(&empty),
            "2dba5dbc339e7316aea2683faf839c1b7b1ee2313db792112588118df066aa35"
        );
    }
}
