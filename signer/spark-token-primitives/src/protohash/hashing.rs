//! Object-hash protobuf serializer.
//!
//! Ported from `runes-spark-bridge/crates/proto-hasher/src/hashing.rs`
//! (which mirrors `spark/common/protohash/hasher.go`). Hashes use `sha2`
//! here instead of `bitcoin::hashes` to avoid adding a heavyweight
//! dependency.

use std::collections::HashMap;

use prost_reflect::{
    DynamicMessage, FieldDescriptor, Kind, MapKey, MessageDescriptor, ReflectMessage, Value,
};
use sha2::{Digest, Sha256};

use super::errors::ProtoHasherError;
use super::google_protobuf::{is_google_proto_value_null, GoogleValue};

const BOOL_IDENTIFIER: &str = "b";
pub(crate) const MAP_IDENTIFIER: &str = "d";
const FLOAT_IDENTIFIER: &str = "f";
const INT_IDENTIFIER: &str = "i";
pub(crate) const LIST_IDENTIFIER: &str = "l";
const BYTE_IDENTIFIER: &str = "r";
const UNICODE_IDENTIFIER: &str = "u";

/// Canonical NaN bit pattern (matches Go's `math.NaN()`).
const NAN_BITS: u64 = 0x7FF8000000000001;

pub(crate) type Hash32 = [u8; 32];

pub(crate) fn hash_fields_by_names(
    message: &DynamicMessage,
    names: &[&str],
) -> Result<Hash32, ProtoHasherError> {
    let mut hasher = Sha256::new();
    hasher.update(LIST_IDENTIFIER.as_bytes());

    for name in names {
        let descriptor = message.descriptor();
        let fd: FieldDescriptor = descriptor
            .get_field_by_name(name)
            .ok_or_else(|| ProtoHasherError::MissingField(name.to_string()))?;

        let value = message.get_field(&fd);
        let value_hash = hash_value(&fd.kind(), value.as_ref())?;

        if let Some(hash) = value_hash {
            hasher.update(hash);
        }
    }

    Ok(finalize(hasher))
}

pub(crate) fn hash_value(kind: &Kind, value: &Value) -> Result<Option<Hash32>, ProtoHasherError> {
    let h = match kind {
        Kind::Bool => {
            let v = value.as_bool().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "bool",
                found: value_type_label(value),
            })?;
            Some(hash_bool(v))
        }
        Kind::Double => {
            let v = value.as_f64().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "double",
                found: value_type_label(value),
            })?;
            Some(hash_f64(v))
        }
        Kind::Float => {
            // prost-reflect represents proto `float` as Value::F32; promote to f64.
            let v = match value {
                Value::F32(v) => *v as f64,
                Value::F64(v) => *v,
                _ => {
                    return Err(ProtoHasherError::ValueTypeMismatch {
                        expected: "float",
                        found: value_type_label(value),
                    })
                }
            };
            Some(hash_f64(v))
        }
        Kind::Int32 | Kind::Sint32 | Kind::Sfixed32 => {
            let v = value.as_i32().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "int32",
                found: value_type_label(value),
            })?;
            Some(hash_i32(v))
        }
        Kind::Int64 | Kind::Sint64 | Kind::Sfixed64 => {
            let v = value.as_i64().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "int64",
                found: value_type_label(value),
            })?;
            Some(hash_i64(v))
        }
        Kind::Uint32 | Kind::Fixed32 => {
            let v = value.as_u32().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "uint32",
                found: value_type_label(value),
            })?;
            Some(hash_u32(v))
        }
        Kind::Uint64 | Kind::Fixed64 => {
            let v = value.as_u64().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "uint64",
                found: value_type_label(value),
            })?;
            Some(hash_u64(v))
        }
        Kind::String => {
            let v = value.as_str().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "string",
                found: value_type_label(value),
            })?;
            Some(hash_string(v))
        }
        Kind::Bytes => {
            let v = value
                .as_bytes()
                .ok_or(ProtoHasherError::ValueTypeMismatch {
                    expected: "bytes",
                    found: value_type_label(value),
                })?;
            Some(hash_bytes(v))
        }
        Kind::Message(_) => {
            let dm = value
                .as_message()
                .ok_or(ProtoHasherError::ValueTypeMismatch {
                    expected: "message",
                    found: value_type_label(value),
                })?;
            hash_message(dm.to_owned())?
        }
        Kind::Enum(_) => {
            let n = value
                .as_enum_number()
                .ok_or(ProtoHasherError::ValueTypeMismatch {
                    expected: "enum",
                    found: value_type_label(value),
                })? as i64;
            Some(hash_i64(n))
        }
    };
    Ok(h)
}

pub(crate) fn hash_bool(b: bool) -> Hash32 {
    let mut hasher = Sha256::new();
    hasher.update(BOOL_IDENTIFIER.as_bytes());
    hasher.update(if b { b"1" } else { b"0" });
    finalize(hasher)
}

pub(crate) fn hash_i32(value: i32) -> Hash32 {
    hash_u64(value as u64)
}

pub(crate) fn hash_i64(value: i64) -> Hash32 {
    hash_u64(value as u64)
}

pub(crate) fn hash_u32(value: u32) -> Hash32 {
    hash_u64(value as u64)
}

pub(crate) fn hash_u64(value: u64) -> Hash32 {
    let mut hasher = Sha256::new();
    hasher.update(INT_IDENTIFIER.as_bytes());
    hasher.update(value.to_be_bytes());
    finalize(hasher)
}

pub(crate) fn hash_f64(value: f64) -> Hash32 {
    let mut f = value;
    if f == 0.0 && f.is_sign_negative() {
        f = 0.0;
    }

    let bits: u64 = if f.is_nan() { NAN_BITS } else { f.to_bits() };

    let mut hasher = Sha256::new();
    hasher.update(FLOAT_IDENTIFIER.as_bytes());
    hasher.update(bits.to_be_bytes());
    finalize(hasher)
}

pub(crate) fn hash_string(value: &str) -> Hash32 {
    let mut hasher = Sha256::new();
    hasher.update(UNICODE_IDENTIFIER.as_bytes());
    hasher.update(value.as_bytes());
    finalize(hasher)
}

pub(crate) fn hash_bytes(bytes: &[u8]) -> Hash32 {
    let mut hasher = Sha256::new();
    hasher.update(BYTE_IDENTIFIER.as_bytes());
    hasher.update(bytes);
    finalize(hasher)
}

/// Hashes a DynamicMessage. Returns `None` if the message has no fields to
/// hash (matches Go's `errSkipField` propagation for empty submessages).
pub fn hash_message<M: Into<DynamicMessage>>(
    message: M,
) -> Result<Option<Hash32>, ProtoHasherError> {
    let message = message.into();
    let descriptor = message.descriptor();

    if let Some(google_value) = GoogleValue::maybe_from_str(descriptor.full_name()) {
        return google_value.hash(&message);
    }

    let mut field_hashes: Vec<FieldHashEntry> = hash_fields(message)?;
    field_hashes.sort_by(|a, b| a.number.cmp(&b.number));

    let mut hasher = Sha256::new();
    hasher.update(MAP_IDENTIFIER.as_bytes());
    for field_hash in field_hashes {
        hasher.update(field_hash.k_hash);
        hasher.update(field_hash.v_hash);
    }

    Ok(Some(finalize(hasher)))
}

fn hash_fields(message: DynamicMessage) -> Result<Vec<FieldHashEntry>, ProtoHasherError> {
    let mut hashes = vec![];

    for (fd, value) in message.fields() {
        if value.is_default(&fd.kind()) {
            continue;
        }

        let Some(hash_entry) = hash_field(&fd, value)? else {
            continue;
        };
        hashes.push(hash_entry);
    }

    Ok(hashes)
}

fn hash_field(
    fd: &FieldDescriptor,
    value: &Value,
) -> Result<Option<FieldHashEntry>, ProtoHasherError> {
    let k_hash = hash_field_key(fd);
    let Some(v_hash) = hash_field_value(fd, value)? else {
        return Ok(None);
    };
    Ok(Some(FieldHashEntry {
        number: fd.number(),
        k_hash,
        v_hash,
    }))
}

pub(crate) fn hash_field_key(fd: &FieldDescriptor) -> Hash32 {
    hash_u32(fd.number())
}

pub(crate) fn hash_field_value(
    fd: &FieldDescriptor,
    value: &Value,
) -> Result<Option<Hash32>, ProtoHasherError> {
    if fd.is_list() {
        let list = value.as_list().ok_or(ProtoHasherError::ValueTypeMismatch {
            expected: "list",
            found: value_type_label(value),
        })?;
        return hash_list(&fd.kind(), list);
    }
    if fd.is_map() {
        let kind = fd.kind();
        let entry_md: &MessageDescriptor = kind
            .as_message()
            .ok_or(ProtoHasherError::MapKindNotMessage)?;
        let key_fd = entry_md.map_entry_key_field();
        let value_fd = entry_md.map_entry_value_field();

        let map_value = value.as_map().ok_or(ProtoHasherError::ValueTypeMismatch {
            expected: "map",
            found: value_type_label(value),
        })?;
        return hash_map(&key_fd, &value_fd, map_value);
    }

    hash_value(&fd.kind(), value)
}

pub(crate) fn hash_list(kind: &Kind, list: &[Value]) -> Result<Option<Hash32>, ProtoHasherError> {
    if list.is_empty() {
        return Ok(None);
    }

    let mut hasher = Sha256::new();
    hasher.update(LIST_IDENTIFIER.as_bytes());

    for value in list {
        if let Some(hash) = hash_value(kind, value)? {
            hasher.update(hash);
        }
    }

    Ok(Some(finalize(hasher)))
}

pub(crate) fn hash_map(
    key_fd: &FieldDescriptor,
    value_fd: &FieldDescriptor,
    map: &HashMap<MapKey, Value>,
) -> Result<Option<Hash32>, ProtoHasherError> {
    let mut hash_entries = vec![];

    for (key, value) in map {
        if is_google_proto_value_null(value) {
            continue;
        }

        let Some(k_hash) = hash_field_value(key_fd, &key.to_owned().into())? else {
            return Ok(None);
        };
        let Some(v_hash) = hash_field_value(value_fd, value)? else {
            return Ok(None);
        };

        hash_entries.push((k_hash, v_hash));
    }

    if hash_entries.is_empty() {
        return Ok(None);
    }

    hash_entries.sort_by(|a, b| a.0.cmp(&b.0));

    let mut hasher = Sha256::new();
    hasher.update(MAP_IDENTIFIER.as_bytes());
    for (k_hash, v_hash) in hash_entries {
        hasher.update(k_hash);
        hasher.update(v_hash);
    }

    Ok(Some(finalize(hasher)))
}

struct FieldHashEntry {
    number: u32,
    k_hash: Hash32,
    v_hash: Hash32,
}

pub(crate) fn value_type_label(v: &Value) -> &'static str {
    match v {
        Value::Bool(_) => "bool",
        Value::I32(_) => "int32",
        Value::I64(_) => "int64",
        Value::U32(_) => "uint32",
        Value::U64(_) => "uint64",
        Value::F32(_) => "float",
        Value::F64(_) => "double",
        Value::String(_) => "string",
        Value::Bytes(_) => "bytes",
        Value::EnumNumber(_) => "enum",
        Value::Message(_) => "message",
        Value::List(_) => "list",
        Value::Map(_) => "map",
    }
}

pub(crate) fn key_type_label(k: &MapKey) -> &'static str {
    match k {
        MapKey::Bool(_) => "bool",
        MapKey::I32(_) => "int32",
        MapKey::I64(_) => "int64",
        MapKey::U32(_) => "uint32",
        MapKey::U64(_) => "uint64",
        MapKey::String(_) => "string",
    }
}

fn finalize(hasher: Sha256) -> Hash32 {
    let digest = hasher.finalize();
    let mut out = [0u8; 32];
    out.copy_from_slice(&digest);
    out
}
