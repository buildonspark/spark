//! Well-known type hashing (google.protobuf.*).
//!
//! Ported from `runes-spark-bridge/crates/proto-hasher/src/google_protobuf.rs`,
//! adapted to use [`sha2`] hashes.

use std::borrow::Cow;

use prost_reflect::{DynamicMessage, FieldDescriptor, OneofDescriptor, ReflectMessage, Value};
use sha2::{Digest, Sha256};

use super::errors::ProtoHasherError;
use super::hashing::{
    hash_bool, hash_f64, hash_fields_by_names, hash_i32, hash_i64, hash_list, hash_message,
    hash_string, hash_u32, hash_u64, key_type_label, value_type_label, Hash32, MAP_IDENTIFIER,
};

const VALUE_NAME: &str = "value";

const NUMBER_VALUE: &str = "number_value";
const STRING_VALUE: &str = "string_value";
const BOOL_VALUE: &str = "bool_value";
const STRUCT_VALUE: &str = "struct_value";
const LIST_VALUE: &str = "list_value";
const NULL_VALUE: &str = "null_value";

pub(crate) enum GoogleValue {
    Any,
    Bool,
    Int32,
    Int64,
    Uint32,
    Uint64,
    Float,
    Double,
    String,
    Struct,
    Timestamp,
    Duration,
    List,
    Value,
}

impl GoogleValue {
    pub fn maybe_from_str(value_str: &str) -> Option<Self> {
        let value = match value_str {
            "google.protobuf.Any" => Self::Any,
            "google.protobuf.BoolValue" => Self::Bool,
            "google.protobuf.FloatValue" => Self::Float,
            "google.protobuf.DoubleValue" => Self::Double,
            "google.protobuf.Int32Value" => Self::Int32,
            "google.protobuf.Int64Value" => Self::Int64,
            "google.protobuf.UInt32Value" => Self::Uint32,
            "google.protobuf.UInt64Value" => Self::Uint64,
            "google.protobuf.StringValue" => Self::String,
            "google.protobuf.Struct" => Self::Struct,
            "google.protobuf.Timestamp" => Self::Timestamp,
            "google.protobuf.Duration" => Self::Duration,
            "google.protobuf.ListValue" => Self::List,
            "google.protobuf.Value" => Self::Value,
            _ => return None,
        };
        Some(value)
    }

    pub fn hash(&self, message: &DynamicMessage) -> Result<Option<Hash32>, ProtoHasherError> {
        match self {
            GoogleValue::Any => hash_google_proto_any(message).map(Some),
            GoogleValue::Bool => hash_google_proto_bool(message).map(Some),
            GoogleValue::Int32 => hash_google_proto_int32(message).map(Some),
            GoogleValue::Int64 => hash_google_proto_int64(message).map(Some),
            GoogleValue::Uint32 => hash_google_proto_u32(message).map(Some),
            GoogleValue::Uint64 => hash_google_proto_u64(message).map(Some),
            GoogleValue::Float => hash_google_proto_float(message).map(Some),
            GoogleValue::Double => hash_google_proto_double(message).map(Some),
            GoogleValue::String => hash_google_proto_string(message).map(Some),
            GoogleValue::Struct => hash_google_proto_struct(message).map(Some),
            GoogleValue::Timestamp => hash_google_proto_timestamp(message).map(Some),
            // Duration shares Timestamp's seconds+nanos layout.
            GoogleValue::Duration => hash_google_proto_timestamp(message).map(Some),
            GoogleValue::List => hash_google_proto_list(message),
            GoogleValue::Value => hash_google_proto_value(message),
        }
    }
}

fn hash_google_proto_any(message: &DynamicMessage) -> Result<Hash32, ProtoHasherError> {
    let type_url = message
        .get_field_by_name("type_url")
        .ok_or_else(|| ProtoHasherError::MissingField("type_url".to_string()))?;

    Err(ProtoHasherError::UnsupportedType(type_url.to_string()))
}

macro_rules! hash_google_proto_primitive {
    ($fn_name:ident, $as_method:ident, $hash_fn:ident, $expected:expr) => {
        fn $fn_name(message: &DynamicMessage) -> Result<Hash32, ProtoHasherError> {
            let v = value_field(message)?;
            let x = v.$as_method().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: $expected,
                found: value_type_label(&v),
            })?;
            Ok($hash_fn(x))
        }
    };
}

hash_google_proto_primitive!(hash_google_proto_bool, as_bool, hash_bool, "bool");
hash_google_proto_primitive!(hash_google_proto_int32, as_i32, hash_i32, "int32");
hash_google_proto_primitive!(hash_google_proto_int64, as_i64, hash_i64, "int64");
hash_google_proto_primitive!(hash_google_proto_u32, as_u32, hash_u32, "uint32");
hash_google_proto_primitive!(hash_google_proto_u64, as_u64, hash_u64, "uint64");
hash_google_proto_primitive!(hash_google_proto_double, as_f64, hash_f64, "double");

fn hash_google_proto_float(message: &DynamicMessage) -> Result<Hash32, ProtoHasherError> {
    let v = value_field(message)?;
    // prost-reflect represents proto `float` as Value::F32; promote to f64.
    let x = match v.as_ref() {
        Value::F32(f) => *f as f64,
        Value::F64(f) => *f,
        _ => {
            return Err(ProtoHasherError::ValueTypeMismatch {
                expected: "float",
                found: value_type_label(&v),
            })
        }
    };
    Ok(hash_f64(x))
}

fn hash_google_proto_string(message: &DynamicMessage) -> Result<Hash32, ProtoHasherError> {
    let v = value_field(message)?;
    let s = v.as_str().ok_or(ProtoHasherError::ValueTypeMismatch {
        expected: "string",
        found: value_type_label(&v),
    })?;
    Ok(hash_string(s))
}

fn hash_google_proto_timestamp(message: &DynamicMessage) -> Result<Hash32, ProtoHasherError> {
    hash_fields_by_names(message, &["seconds", "nanos"])
}

fn hash_google_proto_struct(message: &DynamicMessage) -> Result<Hash32, ProtoHasherError> {
    let fields_value: Cow<Value> = message
        .get_field_by_name("fields")
        .ok_or_else(|| ProtoHasherError::MissingField("fields".to_string()))?;

    let map = fields_value
        .as_map()
        .ok_or(ProtoHasherError::ValueTypeMismatch {
            expected: "map",
            found: value_type_label(&fields_value),
        })?;

    let mut entries = vec![];
    for (key, value) in map {
        let k_hash: Hash32 =
            hash_string(key.as_str().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "string",
                found: key_type_label(key),
            })?);

        if is_google_proto_value_null(value) {
            continue;
        }

        let v_hash = hash_message(
            value
                .as_message()
                .ok_or(ProtoHasherError::ValueTypeMismatch {
                    expected: "message",
                    found: value_type_label(value),
                })?
                .to_owned(),
        )?;

        if let Some(v_hash) = v_hash {
            entries.push((k_hash, v_hash));
        }
    }

    entries.sort_by(|a, b| a.0.cmp(&b.0));

    let mut hasher = Sha256::new();
    hasher.update(MAP_IDENTIFIER.as_bytes());
    for (k_hash, v_hash) in entries {
        hasher.update(k_hash);
        hasher.update(v_hash);
    }

    let digest = hasher.finalize();
    let mut out = [0u8; 32];
    out.copy_from_slice(&digest);
    Ok(out)
}

fn hash_google_proto_list(message: &DynamicMessage) -> Result<Option<Hash32>, ProtoHasherError> {
    let descriptor = message.descriptor();

    let list_values_descriptor = descriptor
        .get_field_by_name("values")
        .ok_or_else(|| ProtoHasherError::MissingField("values".to_string()))?;

    let list_values = message.get_field(&list_values_descriptor);

    let list = list_values
        .as_list()
        .ok_or(ProtoHasherError::ValueTypeMismatch {
            expected: "list",
            found: value_type_label(&list_values),
        })?;

    hash_list(&list_values_descriptor.kind(), list)
}

fn hash_google_proto_value(message: &DynamicMessage) -> Result<Option<Hash32>, ProtoHasherError> {
    let descriptor = message.descriptor();
    let od: OneofDescriptor = descriptor
        .oneofs()
        .find(|o| o.name() == "kind")
        .ok_or_else(|| ProtoHasherError::MissingField("kind".to_string()))?;

    let active: FieldDescriptor = od
        .fields()
        .find(|f| message.has_field(f))
        .ok_or_else(|| ProtoHasherError::EmptyValue("kind".to_string()))?;

    let value = message.get_field(&active);
    let hash = match active.name() {
        NUMBER_VALUE => {
            let n = value.as_f64().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "double",
                found: value_type_label(&value),
            })?;
            Some(hash_f64(n))
        }
        STRING_VALUE => {
            let s = value.as_str().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "string",
                found: value_type_label(&value),
            })?;
            Some(hash_string(s))
        }
        BOOL_VALUE => {
            let b = value.as_bool().ok_or(ProtoHasherError::ValueTypeMismatch {
                expected: "bool",
                found: value_type_label(&value),
            })?;
            Some(hash_bool(b))
        }
        STRUCT_VALUE => {
            let dm = value
                .as_message()
                .ok_or(ProtoHasherError::ValueTypeMismatch {
                    expected: "message",
                    found: value_type_label(&value),
                })?;
            Some(hash_google_proto_struct(dm)?)
        }
        LIST_VALUE => {
            let dm = value
                .as_message()
                .ok_or(ProtoHasherError::ValueTypeMismatch {
                    expected: "message",
                    found: value_type_label(&value),
                })?;
            hash_google_proto_list(dm)?
        }
        NULL_VALUE => {
            return Err(ProtoHasherError::EmptyValue(
                descriptor.full_name().to_string(),
            ))
        }
        _ => return Err(ProtoHasherError::UnexpectedKind(active.name().to_string())),
    };

    Ok(hash)
}

pub(crate) fn is_google_proto_value_null(v: &Value) -> bool {
    let Value::Message(dm) = v else {
        return false;
    };
    if dm.descriptor().full_name() != "google.protobuf.Value" {
        return false;
    }

    let Some(kind) = dm.descriptor().oneofs().find(|o| o.name() == "kind") else {
        return false;
    };

    let active = kind.fields().find(|f| dm.has_field(f));
    match active {
        None => true,
        Some(fd) => fd.name() == "null_value",
    }
}

fn value_field(message: &DynamicMessage) -> Result<Cow<'_, Value>, ProtoHasherError> {
    message
        .get_field_by_name(VALUE_NAME)
        .ok_or_else(|| ProtoHasherError::MissingField(VALUE_NAME.to_string()))
}

#[cfg(test)]
mod tests {
    use prost_reflect::{DescriptorPool, DynamicMessage, Value};
    use prost_types::{
        field_descriptor_proto::{Label, Type},
        DescriptorProto, FieldDescriptorProto, FileDescriptorProto, FileDescriptorSet,
    };

    use crate::protohash::hash_message;

    fn wrapper(name: &str, ty: Type) -> DescriptorProto {
        DescriptorProto {
            name: Some(name.to_string()),
            field: vec![FieldDescriptorProto {
                name: Some("value".to_string()),
                number: Some(1),
                label: Some(Label::Optional as i32),
                r#type: Some(ty as i32),
                ..Default::default()
            }],
            ..Default::default()
        }
    }

    fn wrappers_pool() -> DescriptorPool {
        let file = FileDescriptorProto {
            name: Some("google/protobuf/wrappers.proto".to_string()),
            package: Some("google.protobuf".to_string()),
            syntax: Some("proto3".to_string()),
            message_type: vec![
                wrapper("FloatValue", Type::Float),
                wrapper("DoubleValue", Type::Double),
            ],
            ..Default::default()
        };
        DescriptorPool::from_file_descriptor_set(FileDescriptorSet { file: vec![file] })
            .expect("valid wrapper descriptor set")
    }

    #[test]
    fn float_value_and_double_value_hash_identically() {
        let pool = wrappers_pool();

        let mut float_msg = DynamicMessage::new(
            pool.get_message_by_name("google.protobuf.FloatValue")
                .unwrap(),
        );
        float_msg.set_field_by_name("value", Value::F32(1.5));

        let mut double_msg = DynamicMessage::new(
            pool.get_message_by_name("google.protobuf.DoubleValue")
                .unwrap(),
        );
        double_msg.set_field_by_name("value", Value::F64(1.5));

        let float_hash = hash_message(float_msg).expect("FloatValue must hash without error");
        let double_hash = hash_message(double_msg).expect("DoubleValue must hash without error");

        assert_eq!(
            float_hash, double_hash,
            "a FloatValue and DoubleValue holding the same magnitude must hash identically"
        );
    }
}
