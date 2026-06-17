/// Error types for ProtoHasher operations.
#[derive(Debug, thiserror::Error)]
pub enum ProtoHasherError {
    /// The provided type is not supported for hashing.
    #[error("ProtoHasher does not support hashing of type: {0}")]
    UnsupportedType(String),

    /// A struct value had an unexpected kind.
    #[error("Unexpected struct value kind: {0}")]
    UnexpectedKind(String),

    /// A field was not found in the message.
    #[error("Field is missing: {0}")]
    MissingField(String),

    /// A required field was empty or unset.
    #[error("Missing required field: {0}")]
    EmptyValue(String),

    /// A map field's value was expected to be a message but was not.
    #[error("Map kind is not a message")]
    MapKindNotMessage,

    /// Top-level scalar/value types are not hashable
    #[error("top-level scalar/value types are not hashable; wrap in a parent message field: {0}")]
    TopLevelScalarNotHashable(String),

    /// Nothing to hash
    #[error("Empty or None value is not hashable. Nothing to hash")]
    NothingToHash,

    /// A value's runtime kind didn't match what the hasher expected.
    #[error("value type mismatch: expected {expected}, found {found}")]
    ValueTypeMismatch {
        /// Expected kind label.
        expected: &'static str,
        /// Actual kind label observed.
        found: &'static str,
    },
}
