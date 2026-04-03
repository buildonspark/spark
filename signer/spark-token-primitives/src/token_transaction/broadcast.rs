use prost::Message;

use crate::{
    proto::{
        multisig,
        spark_token::{
            partial_token_transaction, signature_with_index, BroadcastTransactionRequest,
            PartialTokenTransaction, SignatureWithIndex,
        },
    },
    BroadcastBuildRequest, SignatureWithIndexInput, SparkTokenPrimitivesError,
};

use super::validate_length;

pub(crate) fn build_broadcast_transaction_request_impl(
    request: BroadcastBuildRequest,
) -> Result<Vec<u8>, SparkTokenPrimitivesError> {
    validate_length(&request.identity_public_key, 33, "identity_public_key")?;

    let partial_transaction =
        PartialTokenTransaction::decode(request.partial_token_transaction_bytes.as_slice())
            .map_err(|err| {
                SparkTokenPrimitivesError::Spark(format!(
                    "failed to decode PartialTokenTransaction for broadcast request: {err}"
                ))
            })?;

    validate_broadcast_owner_signatures(&partial_transaction, &request.owner_signatures)?;

    let owner_signatures = request
        .owner_signatures
        .into_iter()
        .map(signature_with_index_from_input)
        .collect::<Result<Vec<_>, _>>()?;

    let broadcast_request = BroadcastTransactionRequest {
        identity_public_key: request.identity_public_key,
        partial_token_transaction: Some(partial_transaction),
        token_transaction_owner_signatures: owner_signatures,
    };

    Ok(broadcast_request.encode_to_vec())
}

fn validate_broadcast_owner_signatures(
    partial_transaction: &PartialTokenTransaction,
    owner_signatures: &[SignatureWithIndexInput],
) -> Result<(), SparkTokenPrimitivesError> {
    let token_inputs = partial_transaction.token_inputs.as_ref().ok_or_else(|| {
        SparkTokenPrimitivesError::Spark(
            "partial token transaction is missing token_inputs".to_owned(),
        )
    })?;

    match token_inputs {
        partial_token_transaction::TokenInputs::CreateInput(_) => {
            validate_exactly_one_index_zero_signature(owner_signatures, "createInput")
        }
        partial_token_transaction::TokenInputs::MintInput(_) => {
            validate_exactly_one_index_zero_signature(owner_signatures, "mintInput")
        }
        partial_token_transaction::TokenInputs::TransferInput(transfer_input) => {
            validate_transfer_owner_signatures(
                owner_signatures,
                transfer_input.outputs_to_spend.len(),
            )
        }
    }
}

fn validate_exactly_one_index_zero_signature(
    owner_signatures: &[SignatureWithIndexInput],
    input_case: &str,
) -> Result<(), SparkTokenPrimitivesError> {
    if owner_signatures.len() != 1 {
        return Err(format!(
            "{input_case} partial token transaction requires exactly one owner signature"
        )
        .into());
    }
    if owner_signatures[0].input_index != 0 {
        return Err(format!(
            "{input_case} owner signature must use input_index 0, got {}",
            owner_signatures[0].input_index
        )
        .into());
    }
    Ok(())
}

fn validate_transfer_owner_signatures(
    owner_signatures: &[SignatureWithIndexInput],
    expected_inputs: usize,
) -> Result<(), SparkTokenPrimitivesError> {
    if owner_signatures.len() != expected_inputs {
        return Err(format!(
            "transfer partial token transaction requires exactly {expected_inputs} owner signatures, got {}",
            owner_signatures.len()
        )
        .into());
    }

    let mut seen = vec![false; expected_inputs];
    for signature in owner_signatures {
        let index = signature.input_index as usize;
        if index >= expected_inputs {
            return Err(format!(
                "owner signature input_index {} is out of range for {expected_inputs} transfer inputs",
                signature.input_index
            )
            .into());
        }
        if seen[index] {
            return Err(format!(
                "duplicate owner signature for input_index {}",
                signature.input_index
            )
            .into());
        }
        seen[index] = true;
    }

    for (index, present) in seen.into_iter().enumerate() {
        if !present {
            return Err(format!(
                "missing owner signature for input_index {index}, indexes must be contiguous"
            )
            .into());
        }
    }

    Ok(())
}

fn signature_with_index_from_input(
    input: SignatureWithIndexInput,
) -> Result<SignatureWithIndex, SparkTokenPrimitivesError> {
    validate_length(&input.public_key, 33, "owner_signatures.public_key")?;
    if input.signature.len() < 64 || input.signature.len() > 73 {
        return Err("owner_signatures.signature must be between 64 and 73 bytes".into());
    }

    Ok(SignatureWithIndex {
        signature: None,
        input_index: input.input_index,
        authority_signatures: Some(signature_with_index::AuthoritySignatures::SingleSignature(
            multisig::KeyedSignature {
                public_key: input.public_key,
                signature: input.signature,
            },
        )),
    })
}
