mod broadcast;
mod construct;
mod hash;

pub(crate) use broadcast::build_broadcast_transaction_request_impl;
pub(crate) use construct::construct_partial_transfer_transaction_impl;
pub(crate) use hash::{hash_final_token_transaction_impl, hash_partial_token_transaction_impl};

fn validate_length(
    bytes: &[u8],
    expected_len: usize,
    field_name: &str,
) -> Result<(), crate::SparkTokenPrimitivesError> {
    if bytes.len() != expected_len {
        return Err(format!(
            "{field_name} must be {expected_len} bytes, got {}",
            bytes.len()
        )
        .into());
    }
    Ok(())
}

#[cfg(test)]
mod tests;
