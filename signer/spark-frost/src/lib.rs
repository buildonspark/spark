use frost_secp256k1_tr::Identifier;

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

#[cfg(test)]
mod tests {
    use frost_secp256k1_tr::{
        self as frost,
        keys::{EvenY, Tweak},
        SigningKey, VerifyingKey,
    };
    use rand::thread_rng;
    use std::collections::{BTreeMap, BTreeSet};

    #[test]
    fn test_group_signing() {
        let mut rng = thread_rng();
        let max_signers = 5;
        let min_signers = 3;
        let (shares, pubkey_package) = frost::keys::generate_with_dealer(
            max_signers,
            min_signers,
            frost::keys::IdentifierList::Default,
            &mut rng,
        )
        .unwrap();

        let mut key_packages: BTreeMap<_, _> = BTreeMap::new();

        let user_identifier = frost_secp256k1_tr::Identifier::try_from(100).unwrap();

        let user_key = SigningKey::new(&mut rng.clone());
        let user_pubkey = VerifyingKey::from(&user_key);
        let user_signing_share = frost::keys::SigningShare::new(user_key.to_scalar());
        let user_verifying_share = frost::keys::VerifyingShare::new(user_pubkey.to_element());
        let verifying_key_element =
            pubkey_package.verifying_key().to_element() + user_pubkey.to_element();

        let verifying_key = frost_secp256k1_tr::VerifyingKey::new(verifying_key_element);

        let mut pubkey_shares = pubkey_package.verifying_shares().clone();
        pubkey_shares.insert(user_identifier, user_verifying_share);

        let merkle_root = vec![];

        let aggregate_pubkey_package =
            frost::keys::PublicKeyPackage::new(pubkey_shares, verifying_key);

        let user_key_package = frost::keys::KeyPackage::new(
            user_identifier,
            user_signing_share,
            user_verifying_share,
            verifying_key,
            1,
        );

        let user_key_package_tweaked = user_key_package.clone().tweak(Some(&merkle_root));
        let aggregate_pubkey_package_tweaked =
            aggregate_pubkey_package.clone().tweak(Some(&merkle_root));
        assert_eq!(
            aggregate_pubkey_package_tweaked.verifying_key(),
            user_key_package_tweaked.verifying_key()
        );

        for (identifier, secret_share) in shares {
            let key_package = frost::keys::KeyPackage::try_from(secret_share.clone())
                .unwrap()
                .into_even_y(Some(verifying_key.has_even_y()));
            let new_key_package = frost::keys::KeyPackage::new(
                identifier,
                *key_package.signing_share(),
                *key_package.verifying_share(),
                *user_key_package_tweaked.verifying_key(),
                *key_package.min_signers(),
            );
            key_packages.insert(identifier, new_key_package);
        }

        let mut nonces_map = BTreeMap::new();
        let mut commitments_map = BTreeMap::new();

        // In practice, each iteration of this loop will be executed by its respective participant.
        for participant_index in 1..=min_signers {
            let participant_identifier = participant_index.try_into().expect("should be nonzero");
            let key_package = &key_packages[&participant_identifier];
            // Generate one (1) nonce and one SigningCommitments instance for each
            // participant, up to _threshold_.
            let (nonces, commitments) =
                frost::round1::commit(key_package.signing_share(), &mut rng);
            // In practice, the nonces must be kept by the participant to use in the
            // next round, while the commitment must be sent to the coordinator
            // (or to every other participant if there is no coordinator) using
            // an authenticated channel.
            nonces_map.insert(participant_identifier, nonces);
            commitments_map.insert(participant_identifier, commitments);
        }

        let (nonce, commitments) =
            frost::round1::commit(user_key_package.signing_share(), &mut rng);
        nonces_map.insert(user_identifier, nonce);
        commitments_map.insert(user_identifier, commitments);

        let mut signature_shares = BTreeMap::new();

        let message = "message to sign".as_bytes();
        let participant_keys: BTreeSet<_> = nonces_map
            .keys()
            .filter(|participant_identifier| *participant_identifier != &user_identifier)
            .cloned()
            .collect();
        let mut user_set = BTreeSet::new();
        user_set.insert(user_identifier);

        let signing_participants_groups = vec![participant_keys.clone(), user_set.clone()];

        let signing_package = frost::SigningPackage::new_with_participants_groups(
            commitments_map.clone(),
            Some(signing_participants_groups),
            message,
        );

        for participant_identifier in participant_keys.clone() {
            let key_package = &key_packages[&participant_identifier];

            let nonces = &nonces_map[&participant_identifier];

            // Each participant generates their signature share.
            let signature_share =
                frost::round2::sign(&signing_package, nonces, key_package).unwrap();

            // In practice, the signature share must be sent to the Coordinator
            // using an authenticated channel.
            signature_shares.insert(participant_identifier, signature_share);

            frost::verify_signature_share(
                participant_identifier,
                key_package.verifying_share(),
                &signature_share,
                &signing_package,
                user_key_package_tweaked.verifying_key(),
            )
            .unwrap();
        }

        let user_signature_shard = frost::round2::sign_with_tweak(
            &signing_package,
            &nonces_map[&user_identifier],
            &user_key_package,
            Some(&merkle_root),
        )
        .unwrap();

        frost::verify_signature_share(
            user_identifier,
            user_key_package_tweaked.verifying_share(),
            &user_signature_shard,
            &signing_package,
            user_key_package_tweaked.verifying_key(),
        )
        .unwrap();

        signature_shares.insert(user_identifier, user_signature_shard);

        let operator_signing_package = frost::SigningPackage::new(commitments_map.clone(), message);
        let group_signature = frost::aggregate_with_tweak(
            &operator_signing_package,
            &signature_shares,
            &aggregate_pubkey_package,
            Some(&merkle_root),
        )
        .unwrap();

        let pubkey_package_tweaked = aggregate_pubkey_package.clone().tweak(Some(&merkle_root));
        pubkey_package_tweaked
            .verifying_key()
            .verify(message, &group_signature)
            .expect("signature should be valid for tweaked pubkey_package");
    }
}
