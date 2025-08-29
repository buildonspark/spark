package protohash

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	// Sorted alphabetically by value.
	boolIdentifier     = `b`
	mapIdentifier      = `d`
	floatIdentifier    = `f`
	intIdentifier      = `i`
	listIdentifier     = `l`
	byteIdentifier     = `r`
	unicodeIndentifier = `u`
)

const valueName = protoreflect.Name("value")

type ProtoHasher interface {
	// HashProto returns the object hash of a given protocol buffer message.
	HashProto(msg protoreflect.Message) ([]byte, error)
}

func NewHasher() ProtoHasher {
	return &hasher{}
}

type hasher struct{}

func HashMessage(msg protoreflect.Message) ([]byte, error) {
	return (&hasher{}).HashProto(msg)
}

func Hash(msg proto.Message) ([]byte, error) {
	if msg == nil {
		return nil, fmt.Errorf("cannot hash nil or invalid message")
	}
	return HashMessage(msg.ProtoReflect())
}

var errSkipField = errors.New("skip field")

type fieldHashEntry struct {
	number int32
	khash  []byte
	vhash  []byte
}

func (h *hasher) HashProto(msg protoreflect.Message) ([]byte, error) {
	if msg == nil || !msg.IsValid() {
		return nil, fmt.Errorf("cannot hash nil or invalid message")
	}

	// Make sure the proto itself is actually valid (ie. can be marshalled).
	// If this fails, it probably means there are unset required fields or invalid
	// values.
	if _, err := proto.Marshal(msg.Interface()); err != nil {
		return nil, err
	}

	// Disallow hashing of top-level scalar/value wrapper types.
	// This class is designed to hash entire messages.
	md := msg.Descriptor()
	switch md.FullName() {
	case "google.protobuf.Value",
		"google.protobuf.ListValue",
		"google.protobuf.BoolValue",
		"google.protobuf.Int32Value",
		"google.protobuf.Int64Value",
		"google.protobuf.UInt32Value",
		"google.protobuf.UInt64Value",
		"google.protobuf.FloatValue",
		"google.protobuf.DoubleValue",
		"google.protobuf.StringValue",
		"google.protobuf.BytesValue":
		return nil, fmt.Errorf("top-level scalar/value types are not hashable; wrap in a parent message field: %s", md.FullName())
	}

	return h.hashMessage(msg)
}

func (h *hasher) hashMessage(msg protoreflect.Message) ([]byte, error) {
	if msg == nil {
		return nil, fmt.Errorf("cannot hash nil message")
	}

	md := msg.Descriptor()

	if hash, ok, err := h.hashWellKnownType(md, msg); ok {
		return hash, err
	}
	if md.IsPlaceholder() {
		return nil, nil
	}
	hashes, err := h.hashFields(msg, md.Fields())
	if err != nil {
		return nil, fmt.Errorf("hashing fields: %w", err)
	}
	slices.SortFunc(hashes, func(a, b *fieldHashEntry) int {
		return cmp.Compare(a.number, b.number)
	})

	ha := sha256.New()
	_, _ = ha.Write([]byte(mapIdentifier))
	for _, hash := range hashes {
		_, _ = ha.Write(hash.khash)
		_, _ = ha.Write(hash.vhash)
	}

	return ha.Sum(nil), nil
}

func (h *hasher) hashFields(msg protoreflect.Message, fields protoreflect.FieldDescriptors) ([]*fieldHashEntry, error) {
	hashes := make([]*fieldHashEntry, 0, fields.Len())

	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !msg.Has(fd) {
			// if we are in this block and the field is a scalar one, it is
			// either a proto3 field that was never set or is the empty value
			// (indistinguishable) or this is a proto2 field that is nil.
			continue
		}
		value := msg.Get(fd)
		if isDefault(fd, value) {
			continue
		}
		hash, err := h.hashField(fd, value)
		if err != nil {
			return nil, err
		}
		if hash == nil {
			// Field chose to skip itself (e.g., map with zero effective entries or empty list)
			continue
		}
		hashes = append(hashes, hash)
	}

	return hashes, nil
}

func isDefault(fd protoreflect.FieldDescriptor, value protoreflect.Value) bool {
	if fd.IsList() {
		return value.List().Len() == 0
	}
	if fd.IsMap() {
		return value.Map().Len() == 0
	}
	// Special case for google.protobuf.Value containing NULL.
	if fd.Kind() == protoreflect.MessageKind && fd.Message() != nil && fd.Message().FullName() == "google.protobuf.Value" {
		msg := value.Message()
		// An empty Value message, or one explicitly set to NullValue, is considered default.
		oneofDesc := msg.Descriptor().Oneofs().ByName("kind")
		fieldDesc := msg.WhichOneof(oneofDesc)
		if fieldDesc == nil || fieldDesc.Name() == "null_value" {
			return true
		}
	}

	switch fd.Kind() {
	case protoreflect.BoolKind:
		return !value.Bool()
	case protoreflect.EnumKind:
		return value.Enum() == 0
	case
		protoreflect.Int32Kind,
		protoreflect.Int64Kind,
		protoreflect.Sint32Kind,
		protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind,
		protoreflect.Sfixed64Kind:
		return value.Int() == 0
	case
		protoreflect.Uint32Kind,
		protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind,
		protoreflect.Fixed64Kind:
		return value.Uint() == 0
	case
		protoreflect.FloatKind,
		protoreflect.DoubleKind:
		return value.Float() == 0
	case protoreflect.StringKind:
		return value.String() == ""
	case protoreflect.BytesKind:
		return len(value.Bytes()) == 0
	case protoreflect.MessageKind:
		// Message fields are never considered default when present
		return false
	case protoreflect.GroupKind:
		// Group fields are never considered default when present
		return false
	}
	return false
}

func (h *hasher) hashField(fd protoreflect.FieldDescriptor, value protoreflect.Value) (*fieldHashEntry, error) {
	khash := h.hashFieldKey(fd)

	vhash, err := h.hashFieldValue(fd, value)
	if err != nil {
		if errors.Is(err, errSkipField) {
			// Instruct caller to skip emitting this field entirely
			return nil, nil
		}
		return nil, fmt.Errorf("hashing field value %d (%s): %w", fd.Number(), fd.FullName(), err)
	}

	return &fieldHashEntry{
		number: int32(fd.Number()),
		khash:  khash,
		vhash:  vhash,
	}, nil
}

func (h *hasher) hashFieldKey(fd protoreflect.FieldDescriptor) []byte {
	return h.hashInt(int64(fd.Number()))
}

func (h *hasher) hashFieldValue(fd protoreflect.FieldDescriptor, value protoreflect.Value) ([]byte, error) {
	if fd.IsList() {
		return h.hashList(fd.Kind(), value.List())
	}
	if fd.IsMap() {
		return h.hashMap(fd.MapKey(), fd.MapValue(), value.Map())
	}
	return h.hashValue(fd.Kind(), value)
}

func (h *hasher) hashValue(kind protoreflect.Kind, value protoreflect.Value) ([]byte, error) {
	switch kind {
	case
		protoreflect.BoolKind:
		return h.hashBool(value.Bool()), nil
	case
		protoreflect.EnumKind:
		return h.hashEnum(value.Enum()), nil
	case
		protoreflect.Uint32Kind,
		protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind,
		protoreflect.Fixed64Kind:
		return h.hashUint(value.Uint()), nil
	case
		protoreflect.Int32Kind,
		protoreflect.Int64Kind,
		protoreflect.Sint32Kind,
		protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind,
		protoreflect.Sfixed64Kind:
		return h.hashInt(value.Int()), nil
	case
		protoreflect.FloatKind,
		protoreflect.DoubleKind:
		return h.hashFloat(value.Float()), nil
	case
		protoreflect.StringKind:
		return h.hashString(value.String()), nil
	case
		protoreflect.BytesKind:
		return h.hashBytes(value.Bytes()), nil
	case
		protoreflect.MessageKind:
		return h.hashMessage(value.Message())
	case
		protoreflect.GroupKind:
		return nil, fmt.Errorf("protoreflect.GroupKind: not implemented: %T", value)
	}
	return nil, fmt.Errorf("unexpected field kind: %v (%T)", kind, value)
}

func (h *hasher) hashBool(b bool) []byte {
	bb := []byte(`0`)
	if b {
		bb = []byte(`1`)
	}
	return hash(boolIdentifier, bb)
}

func (h *hasher) hashEnum(value protoreflect.EnumNumber) []byte {
	return h.hashInt(int64(value))
}

func (h *hasher) hashInt(value int64) []byte {
	return hash(intIdentifier, binary.BigEndian.AppendUint64(nil, uint64(value)))
}

func (h *hasher) hashUint(value uint64) []byte {
	return hash(intIdentifier, binary.BigEndian.AppendUint64(nil, value))
}

func (h *hasher) hashFloat(f float64) []byte {
	// Normalize -0.0 to 0.0 to ensure they hash to the same value,
	// as they are semantically equal in most contexts.
	if f == 0 && math.Signbit(f) {
		f = 0
	}

	// Use the canonical NaN representation for all NaN values.
	if math.IsNaN(f) {
		f = math.NaN()
	}

	bits := math.Float64bits(f)
	byteSlice := binary.BigEndian.AppendUint64(nil, bits)
	return hash(floatIdentifier, byteSlice)
}

func (h *hasher) hashString(s string) []byte {
	return hash(unicodeIndentifier, []byte(s))
}

func (h *hasher) hashBytes(bs []byte) []byte {
	return hash(byteIdentifier, bs)
}

func (h *hasher) hashList(kind protoreflect.Kind, list protoreflect.List) ([]byte, error) {
	if list.Len() == 0 {
		return nil, errSkipField
	}

	ha := sha256.New()
	if _, err := ha.Write([]byte(listIdentifier)); err != nil {
		return nil, err
	}

	for i := 0; i < list.Len(); i++ {
		value := list.Get(i)
		data, err := h.hashValue(kind, value)
		if err != nil {
			return nil, fmt.Errorf("hashing list item %d: %w", i, err)
		}
		if _, err := ha.Write(data); err != nil {
			return nil, err
		}
	}

	return ha.Sum(nil), nil
}

func (h *hasher) hashMap(kd, fd protoreflect.FieldDescriptor, m protoreflect.Map) ([]byte, error) {
	var mapHashEntries []hashMapEntry
	var errValue error
	var errKey protoreflect.MapKey
	m.Range(func(mk protoreflect.MapKey, v protoreflect.Value) bool {
		// Only skip entries where the VALUE is google.protobuf.Value set to null.
		if fd.Kind() == protoreflect.MessageKind && fd.Message() != nil && fd.Message().FullName() == "google.protobuf.Value" {
			msg := v.Message()
			od := msg.Descriptor().Oneofs().ByName("kind")
			fdesc := msg.WhichOneof(od)
			if fdesc == nil || fdesc.Name() == "null_value" {
				return true
			}
		}

		khash, err := h.hashFieldValue(kd, mk.Value())
		if err != nil {
			errKey = mk
			errValue = err
			return false
		}

		vhash, err := h.hashFieldValue(fd, v)
		if err != nil {
			errKey = mk
			errValue = err
			return false
		}

		mapHashEntries = append(mapHashEntries, hashMapEntry{
			khash: khash,
			vhash: vhash,
		})

		return true
	})
	if errValue != nil {
		return nil, fmt.Errorf("hashing map key %v: %w", errKey, errValue)
	}

	if len(mapHashEntries) == 0 {
		return nil, errSkipField
	}

	slices.SortFunc(mapHashEntries, func(a, b hashMapEntry) int { return bytes.Compare(a.khash, b.khash) })

	ha := sha256.New()
	if _, err := ha.Write([]byte(mapIdentifier)); err != nil {
		return nil, err
	}
	for _, e := range mapHashEntries {
		if _, err := ha.Write(e.khash[:]); err != nil {
			return nil, err
		}
		if _, err := ha.Write(e.vhash[:]); err != nil {
			return nil, err
		}
	}

	return ha.Sum(nil), nil
}

func (h *hasher) hashWellKnownType(md protoreflect.MessageDescriptor, msg protoreflect.Message) (hash []byte, ok bool, err error) {
	fullName := md.FullName()
	switch fullName {
	case "google.protobuf.Any":
		hash, err = h.hashGoogleProtobufAny(md, msg)
	case "google.protobuf.BoolValue":
		hash, err = h.hashGoogleProtobufBoolValue(md, msg)
	case "google.protobuf.DoubleValue":
		hash, err = h.hashGoogleProtobufDoubleValue(md, msg)
	case "google.protobuf.Duration":
		hash, err = h.hashGoogleProtobufDuration(md, msg)
	case "google.protobuf.FloatValue":
		hash, err = h.hashGoogleProtobufFloatValue(md, msg)
	case "google.protobuf.Int32Value":
		hash, err = h.hashGoogleProtobufInt32Value(md, msg)
	case "google.protobuf.ListValue":
		hash, err = h.hashGoogleProtobufListValue(md, msg)
	case "google.protobuf.Int64Value":
		hash, err = h.hashGoogleProtobufInt64Value(md, msg)
	case "google.protobuf.StringValue":
		hash, err = h.hashGoogleProtobufStringValue(md, msg)
	case "google.protobuf.Struct":
		hash, err = h.hashGoogleProtobufStruct(md, msg)
	case "google.protobuf.Timestamp":
		hash, err = h.hashGoogleProtobufTimestamp(md, msg)
	case "google.protobuf.UInt32Value":
		hash, err = h.hashGoogleProtobufUint32Value(md, msg)
	case "google.protobuf.UInt64Value":
		hash, err = h.hashGoogleProtobufUint64Value(md, msg)
	case "google.protobuf.Value":
		hash, err = h.hashGoogleProtobufValue(md, msg)
	default:
		return nil, false, nil // no special handling needed, use hashMessage
	}
	return hash, true, err
}

func (h *hasher) hashGoogleProtobufAny(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	typeUrl := msg.Get(md.Fields().ByName("type_url")).String()
	return nil, fmt.Errorf("protoreflecthash does not support hashing of Any type: %s", typeUrl)
}

func (h *hasher) hashGoogleProtobufDuration(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashFieldsByName(md, msg, "seconds", "nanos")
}

func (h *hasher) hashGoogleProtobufTimestamp(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashFieldsByName(md, msg, "seconds", "nanos")
}

func (h *hasher) hashFieldsByName(md protoreflect.MessageDescriptor, msg protoreflect.Message, names ...string) ([]byte, error) {
	ha := sha256.New()
	_, _ = ha.Write([]byte(listIdentifier))

	for _, name := range names {
		value := msg.Get(md.Fields().ByName(protoreflect.Name(name)))
		data, err := h.hashValue(protoreflect.Int32Kind, value)
		if err != nil {
			return nil, fmt.Errorf("hashing %s: %w", md.FullName(), err)
		}
		if _, err := ha.Write(data); err != nil {
			return nil, err
		}
	}

	return ha.Sum(nil), nil
}

func (h *hasher) hashGoogleProtobufDoubleValue(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashFloat(msg.Get(md.Fields().ByName(valueName)).Float()), nil
}

func (h *hasher) hashGoogleProtobufFloatValue(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashFloat(msg.Get(md.Fields().ByName(valueName)).Float()), nil
}

func (h *hasher) hashGoogleProtobufInt32Value(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashInt(msg.Get(md.Fields().ByName(valueName)).Int()), nil
}

func (h *hasher) hashGoogleProtobufInt64Value(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashInt(msg.Get(md.Fields().ByName(valueName)).Int()), nil
}

func (h *hasher) hashGoogleProtobufUint32Value(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashUint(msg.Get(md.Fields().ByName(valueName)).Uint()), nil
}

func (h *hasher) hashGoogleProtobufUint64Value(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashUint(msg.Get(md.Fields().ByName(valueName)).Uint()), nil
}

func (h *hasher) hashGoogleProtobufBoolValue(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashBool(msg.Get(md.Fields().ByName(valueName)).Bool()), nil
}

func (h *hasher) hashGoogleProtobufStringValue(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	return h.hashString(msg.Get(md.Fields().ByName(valueName)).String()), nil
}

func (h *hasher) hashGoogleProtobufValue(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	od := md.Oneofs().ByName("kind")
	fd := msg.WhichOneof(od)
	if fd == nil {
		return nil, fmt.Errorf("invalid struct value: one value must be populated")
	}
	value := msg.Get(fd)

	switch fd.Name() {
	case "null_value":
		return nil, fmt.Errorf("cannot hash nil value")
	case "number_value":
		return h.hashFloat(value.Float()), nil
	case "string_value":
		return h.hashString(value.String()), nil
	case "bool_value":
		return h.hashBool(value.Bool()), nil
	case "struct_value":
		return h.hashGoogleProtobufStruct(value.Message().Descriptor(), value.Message())
	case "list_value":
		return h.hashGoogleProtobufListValue(value.Message().Descriptor(), value.Message())
	default:
		return nil, fmt.Errorf("unexpected struct value kind: %s", fd.Name())
	}
}

func (h *hasher) hashGoogleProtobufListValue(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	list := msg.Get(md.Fields().ByName("values")).List()
	return h.hashList(protoreflect.MessageKind, list)
}

func (h *hasher) hashGoogleProtobufStruct(md protoreflect.MessageDescriptor, msg protoreflect.Message) ([]byte, error) {
	m := msg.Get(md.Fields().ByName("fields")).Map()
	var mapHashEntries []hashMapEntry

	var errValue error
	var errKey protoreflect.MapKey
	m.Range(func(mk protoreflect.MapKey, v protoreflect.Value) bool {
		khash := h.hashString(mk.String())

		// Only skip entries where the VALUE is google.protobuf.Value set to null
		val := v.Message()
		od := val.Descriptor().Oneofs().ByName("kind")
		fdesc := val.WhichOneof(od)
		if fdesc == nil || fdesc.Name() == "null_value" {
			return true
		}

		vhash, err := h.hashMessage(val)
		if err != nil {
			if errors.Is(err, errSkipField) {
				return true
			}
			errKey = mk
			errValue = err
			return false
		}

		mapHashEntries = append(mapHashEntries, hashMapEntry{
			khash: khash,
			vhash: vhash,
		})

		return true
	})
	if errValue != nil {
		return nil, fmt.Errorf("hashing map key %v: %w", errKey, errValue)
	}

	slices.SortFunc(mapHashEntries, func(a, b hashMapEntry) int { return bytes.Compare(a.khash, b.khash) })

	ha := sha256.New()
	_, _ = ha.Write([]byte(mapIdentifier))
	for _, e := range mapHashEntries {
		_, _ = ha.Write(e.khash[:])
		_, _ = ha.Write(e.vhash[:])
	}

	return ha.Sum(nil), nil
}

type hashMapEntry struct {
	khash []byte
	vhash []byte
}

func hash(t string, b []byte) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte(t))
	_, _ = h.Write(b)
	result := h.Sum(nil)
	return result
}
