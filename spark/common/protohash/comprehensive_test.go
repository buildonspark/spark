package protohash

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHashWellKnownTypes(t *testing.T) {

	t.Run("Duration", func(t *testing.T) {
		msg1 := &durationpb.Duration{Seconds: 60, Nanos: 0}   // 1 minute
		msg2 := &durationpb.Duration{Seconds: 3600, Nanos: 0} // 1 hour
		msg3 := &durationpb.Duration{Seconds: 60, Nanos: 0}   // 1 minute

		hash1, err := HashMessage(msg1.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Duration: %v", err)
		}

		hash2, err := HashMessage(msg2.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Duration: %v", err)
		}

		hash3, err := HashMessage(msg3.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Duration: %v", err)
		}

		// Same values should have same hash
		if string(hash1) != string(hash3) {
			t.Error("Same Duration should have same hash")
		}

		// Different values should have different hash
		if string(hash1) == string(hash2) {
			t.Error("Different Duration should have different hash")
		}
	})

	t.Run("Timestamp", func(t *testing.T) {
		msg1 := &timestamppb.Timestamp{Seconds: 1234567890, Nanos: 0}
		msg2 := &timestamppb.Timestamp{Seconds: 1234567891, Nanos: 0}
		msg3 := &timestamppb.Timestamp{Seconds: 1234567890, Nanos: 0}

		hash1, err := HashMessage(msg1.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Timestamp: %v", err)
		}

		hash2, err := HashMessage(msg2.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Timestamp: %v", err)
		}

		hash3, err := HashMessage(msg3.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Timestamp: %v", err)
		}

		// Same values should have same hash
		if string(hash1) != string(hash3) {
			t.Error("Same Timestamp should have same hash")
		}

		// Different values should have different hash
		if string(hash1) == string(hash2) {
			t.Error("Different Timestamp should have different hash")
		}
	})
}

func TestHashStructValue(t *testing.T) {

	t.Run("Empty struct", func(t *testing.T) {
		msg := &structpb.Struct{Fields: map[string]*structpb.Value{}}

		hash, err := HashMessage(msg.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash empty Struct: %v", err)
		}

		if len(hash) == 0 {
			t.Error("Expected non-empty hash for empty struct")
		}
	})

	t.Run("Struct with values", func(t *testing.T) {
		msg1 := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"name": {Kind: &structpb.Value_StringValue{StringValue: "test"}},
				"age":  {Kind: &structpb.Value_NumberValue{NumberValue: 30}},
			},
		}

		msg2 := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"name": {Kind: &structpb.Value_StringValue{StringValue: "test"}},
				"age":  {Kind: &structpb.Value_NumberValue{NumberValue: 30}},
			},
		}

		msg3 := &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"name": {Kind: &structpb.Value_StringValue{StringValue: "different"}},
				"age":  {Kind: &structpb.Value_NumberValue{NumberValue: 30}},
			},
		}

		hash1, err := HashMessage(msg1.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Struct: %v", err)
		}

		hash2, err := HashMessage(msg2.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Struct: %v", err)
		}

		hash3, err := HashMessage(msg3.ProtoReflect())
		if err != nil {
			t.Fatalf("Failed to hash Struct: %v", err)
		}

		// Same structs should have same hash
		if string(hash1) != string(hash2) {
			t.Error("Same Struct should have same hash")
		}

		// Different structs should have different hash
		if string(hash1) == string(hash3) {
			t.Error("Different Struct should have different hash")
		}
	})
}

// Tests using google.protobuf.Struct to exercise non-WKT message behaviors
// These tests focus on the core hashing behaviors without Spark-specific dependencies

func TestNonWellKnownType_DefaultSkipping(t *testing.T) {

	// Empty struct - all fields are default
	empty := &structpb.Struct{
		Fields: map[string]*structpb.Value{},
	}

	// Struct with explicit null values (should be treated as default)
	withNulls := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"null_field": {Kind: &structpb.Value_NullValue{}},
		},
	}

	hEmpty, err := HashMessage(empty.ProtoReflect())
	if err != nil {
		t.Fatalf("hash empty: %v", err)
	}

	hWithNulls, err := HashMessage(withNulls.ProtoReflect())
	if err != nil {
		t.Fatalf("hash with nulls: %v", err)
	}

	// Null values should be treated as default and skipped
	if string(hEmpty) != string(hWithNulls) {
		t.Errorf("null values should be skipped; hashes differ")
	}
}

func TestNonWellKnownType_FieldSortingByNumber(t *testing.T) {

	// Create structs with same content but different field insertion order
	// Note: map iteration order is not guaranteed, but the hasher should sort by field number
	struct1 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"name":   {Kind: &structpb.Value_StringValue{StringValue: "test"}},
			"age":    {Kind: &structpb.Value_NumberValue{NumberValue: 30}},
			"active": {Kind: &structpb.Value_BoolValue{BoolValue: true}},
		},
	}

	struct2 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"active": {Kind: &structpb.Value_BoolValue{BoolValue: true}},
			"name":   {Kind: &structpb.Value_StringValue{StringValue: "test"}},
			"age":    {Kind: &structpb.Value_NumberValue{NumberValue: 30}},
		},
	}

	h1, err := HashMessage(struct1.ProtoReflect())
	if err != nil {
		t.Fatalf("hash struct1: %v", err)
	}

	h2, err := HashMessage(struct2.ProtoReflect())
	if err != nil {
		t.Fatalf("hash struct2: %v", err)
	}

	// Field order should not affect hash due to sorting
	if string(h1) != string(h2) {
		t.Errorf("field order should not affect hash; got different hashes")
	}
}

func TestNonWellKnownType_NestedMessages(t *testing.T) {

	// Test nested struct handling
	nested1 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"user": {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{
						Fields: map[string]*structpb.Value{
							"name": {Kind: &structpb.Value_StringValue{StringValue: "alice"}},
							"id":   {Kind: &structpb.Value_NumberValue{NumberValue: 123}},
						},
					},
				},
			},
		},
	}

	nested2 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"user": {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{
						Fields: map[string]*structpb.Value{
							"name": {Kind: &structpb.Value_StringValue{StringValue: "alice"}},
							"id":   {Kind: &structpb.Value_NumberValue{NumberValue: 123}},
						},
					},
				},
			},
		},
	}

	nested3 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"user": {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{
						Fields: map[string]*structpb.Value{
							"name": {Kind: &structpb.Value_StringValue{StringValue: "bob"}}, // Different nested value
							"id":   {Kind: &structpb.Value_NumberValue{NumberValue: 123}},
						},
					},
				},
			},
		},
	}

	h1, err := HashMessage(nested1.ProtoReflect())
	if err != nil {
		t.Fatalf("hash nested1: %v", err)
	}

	h2, err := HashMessage(nested2.ProtoReflect())
	if err != nil {
		t.Fatalf("hash nested2: %v", err)
	}

	h3, err := HashMessage(nested3.ProtoReflect())
	if err != nil {
		t.Fatalf("hash nested3: %v", err)
	}

	// Same nested messages should have same hash
	if string(h1) != string(h2) {
		t.Errorf("same nested messages should have same hash")
	}

	// Different nested messages should have different hash
	if string(h1) == string(h3) {
		t.Errorf("different nested messages should have different hashes")
	}
}

func TestNonWellKnownType_ListHandling(t *testing.T) {

	// Test list field handling
	list1 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"items": {
				Kind: &structpb.Value_ListValue{
					ListValue: &structpb.ListValue{
						Values: []*structpb.Value{
							{Kind: &structpb.Value_StringValue{StringValue: "a"}},
							{Kind: &structpb.Value_StringValue{StringValue: "b"}},
							{Kind: &structpb.Value_StringValue{StringValue: "c"}},
						},
					},
				},
			},
		},
	}

	list2 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"items": {
				Kind: &structpb.Value_ListValue{
					ListValue: &structpb.ListValue{
						Values: []*structpb.Value{
							{Kind: &structpb.Value_StringValue{StringValue: "a"}},
							{Kind: &structpb.Value_StringValue{StringValue: "b"}},
							{Kind: &structpb.Value_StringValue{StringValue: "c"}},
						},
					},
				},
			},
		},
	}

	list3 := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"items": {
				Kind: &structpb.Value_ListValue{
					ListValue: &structpb.ListValue{
						Values: []*structpb.Value{
							{Kind: &structpb.Value_StringValue{StringValue: "c"}}, // Different order
							{Kind: &structpb.Value_StringValue{StringValue: "b"}},
							{Kind: &structpb.Value_StringValue{StringValue: "a"}},
						},
					},
				},
			},
		},
	}

	emptyList := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"items": {
				Kind: &structpb.Value_ListValue{
					ListValue: &structpb.ListValue{Values: []*structpb.Value{}},
				},
			},
		},
	}

	noList := &structpb.Struct{
		Fields: map[string]*structpb.Value{},
	}

	h1, err := HashMessage(list1.ProtoReflect())
	if err != nil {
		t.Fatalf("hash list1: %v", err)
	}

	h2, err := HashMessage(list2.ProtoReflect())
	if err != nil {
		t.Fatalf("hash list2: %v", err)
	}

	h3, err := HashMessage(list3.ProtoReflect())
	if err != nil {
		t.Fatalf("hash list3: %v", err)
	}

	hEmpty, err := HashMessage(emptyList.ProtoReflect())
	if err != nil {
		t.Fatalf("hash empty list: %v", err)
	}

	hNo, err := HashMessage(noList.ProtoReflect())
	if err != nil {
		t.Fatalf("hash no list: %v", err)
	}

	// Same lists should have same hash
	if string(h1) != string(h2) {
		t.Errorf("same lists should have same hash")
	}

	// Different order should produce different hash (lists are ordered)
	if string(h1) == string(h3) {
		t.Errorf("different list order should produce different hashes")
	}

	// Empty list and no list should have same hash (both default)
	if string(hEmpty) != string(hNo) {
		t.Errorf("empty list and no list should have same hash")
	}
}

func TestNonWellKnownType_ValueTypes(t *testing.T) {

	// Test different Value types
	stringVal := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"field": {Kind: &structpb.Value_StringValue{StringValue: "test"}},
		},
	}

	numberVal := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"field": {Kind: &structpb.Value_NumberValue{NumberValue: 42}},
		},
	}

	boolVal := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"field": {Kind: &structpb.Value_BoolValue{BoolValue: true}},
		},
	}

	hString, err := HashMessage(stringVal.ProtoReflect())
	if err != nil {
		t.Fatalf("hash string: %v", err)
	}

	hNumber, err := HashMessage(numberVal.ProtoReflect())
	if err != nil {
		t.Fatalf("hash number: %v", err)
	}

	hBool, err := HashMessage(boolVal.ProtoReflect())
	if err != nil {
		t.Fatalf("hash bool: %v", err)
	}

	// Different value types should produce different hashes
	hashes := map[string]string{
		"string": string(hString),
		"number": string(hNumber),
		"bool":   string(hBool),
	}

	for name1, hash1 := range hashes {
		for name2, hash2 := range hashes {
			if name1 != name2 && hash1 == hash2 {
				t.Errorf("different value types %s and %s should have different hashes", name1, name2)
			}
		}
	}
}

// ----------------------
// Dynamic KV test message
// ----------------------

var kvDescriptorCache protoreflect.MessageDescriptor

func getKVDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	if kvDescriptorCache != nil {
		return kvDescriptorCache
	}

	file := &descriptorpb.FileDescriptorProto{
		Syntax:  proto.String("proto3"),
		Name:    proto.String("test_kv.proto"),
		Package: proto.String("protohash.test"),
		Dependency: []string{
			"google/protobuf/struct.proto",
		},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("KV"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("int32"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum()},
					{Name: proto.String("bool"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum()},
					{Name: proto.String("string"), Number: proto.Int32(3), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					{Name: proto.String("bytes"), Number: proto.Int32(4), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum()},
					{Name: proto.String("value"), Number: proto.Int32(5), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".google.protobuf.Value")},
					{Name: proto.String("double"), Number: proto.Int32(6), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE.Enum()},
					{Name: proto.String("strings"), Number: proto.Int32(7), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
					{Name: proto.String("string_map"), Number: proto.Int32(8), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String("protohash.test.KV.StringMapEntry")},
					{Name: proto.String("value_map"), Number: proto.Int32(9), Label: descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String("protohash.test.KV.ValueMapEntry")},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					{
						Name:    proto.String("StringMapEntry"),
						Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("key"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
							{Name: proto.String("value"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
						},
					},
					{
						Name:    proto.String("ValueMapEntry"),
						Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("key"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()},
							{Name: proto.String("value"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String(".google.protobuf.Value")},
						},
					},
				},
			},
		},
	}

	var reg protoregistry.Files
	_ = reg.RegisterFile(structpb.File_google_protobuf_struct_proto)

	fd, err := protodesc.NewFile(file, &reg)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	md := fd.Messages().ByName("KV")
	if md == nil {
		t.Fatalf("message KV not found")
	}
	kvDescriptorCache = md
	return md
}

func newKV(defaults bool) *dynamicpb.Message {
	md := getKVDescriptor(&testing.T{})
	m := dynamicpb.NewMessage(md)
	if defaults {
		m.Set(md.Fields().ByNumber(1), protoreflect.ValueOfInt32(0))
		m.Set(md.Fields().ByNumber(2), protoreflect.ValueOfBool(false))
		m.Set(md.Fields().ByNumber(3), protoreflect.ValueOfString(""))
		m.Set(md.Fields().ByNumber(4), protoreflect.ValueOfBytes([]byte{}))
		nullVal := structpb.NewNullValue()
		m.Set(md.Fields().ByNumber(5), protoreflect.ValueOfMessage(nullVal.ProtoReflect()))
		m.Set(md.Fields().ByNumber(6), protoreflect.ValueOfFloat64(0))
		_ = m.Mutable(md.Fields().ByNumber(7)).List()
		_ = m.Mutable(md.Fields().ByNumber(8)).Map()
		_ = m.Mutable(md.Fields().ByNumber(9)).Map()
	}
	return m
}

func TestKV_DefaultSkipping_Equivalence(t *testing.T) {

	base := newKV(false)
	explicit := newKV(true)

	hBase, err := HashMessage(base)
	if err != nil {
		t.Fatalf("hash base: %v", err)
	}
	hExplicit, err := HashMessage(explicit)
	if err != nil {
		t.Fatalf("hash explicit: %v", err)
	}
	if string(hBase) != string(hExplicit) {
		t.Errorf("explicit defaults should be skipped; hashes differ")
	}
}

func TestKV_DefaultsEqualBase(t *testing.T) {
	md := getKVDescriptor(t)

	type setter func(m *dynamicpb.Message)
	tests := []struct {
		name       string
		setDefault setter
	}{
		{name: "int32", setDefault: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(1), protoreflect.ValueOfInt32(0)) }},
		{name: "bool", setDefault: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(2), protoreflect.ValueOfBool(false)) }},
		{name: "string", setDefault: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(3), protoreflect.ValueOfString("")) }},
		{name: "bytes", setDefault: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(4), protoreflect.ValueOfBytes([]byte{})) }},
		{name: "value", setDefault: func(m *dynamicpb.Message) {
			m.Set(md.Fields().ByNumber(5), protoreflect.ValueOfMessage(structpb.NewNullValue().ProtoReflect()))
		}},
		{name: "double", setDefault: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(6), protoreflect.ValueOfFloat64(0)) }},
		{name: "string-list", setDefault: func(m *dynamicpb.Message) { _ = m.Mutable(md.Fields().ByNumber(7)).List() }},
		{name: "string_map", setDefault: func(m *dynamicpb.Message) { _ = m.Mutable(md.Fields().ByNumber(8)).Map() }},
		{name: "value_map", setDefault: func(m *dynamicpb.Message) { _ = m.Mutable(md.Fields().ByNumber(9)).Map() }},
		{name: "value_map-null-entry", setDefault: func(m *dynamicpb.Message) {
			mp := m.Mutable(md.Fields().ByNumber(9)).Map()
			mp.Set(protoreflect.MapKey(protoreflect.ValueOfString("k")), protoreflect.ValueOfMessage(structpb.NewNullValue().ProtoReflect()))
		}},
	}

	base := newKV(false)
	hBase, err := HashMessage(base)
	if err != nil {
		t.Fatalf("hash base: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newKV(false)
			tc.setDefault(m)
			h, err := HashMessage(m)
			if err != nil {
				t.Fatalf("hash default: %v", err)
			}
			if string(h) != string(hBase) {
				t.Fatalf("explicit default should equal base")
			}
		})
	}
}

func TestKV_NonDefaultsDiffer(t *testing.T) {
	md := getKVDescriptor(t)

	type setter func(m *dynamicpb.Message)
	tests := []struct {
		name      string
		setNonDef setter
	}{
		{name: "int32", setNonDef: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(1), protoreflect.ValueOfInt32(1)) }},
		{name: "bool", setNonDef: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(2), protoreflect.ValueOfBool(true)) }},
		{name: "string", setNonDef: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(3), protoreflect.ValueOfString("x")) }},
		{name: "bytes", setNonDef: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(4), protoreflect.ValueOfBytes([]byte{1})) }},
		{name: "value", setNonDef: func(m *dynamicpb.Message) {
			m.Set(md.Fields().ByNumber(5), protoreflect.ValueOfMessage(structpb.NewStringValue("x").ProtoReflect()))
		}},
		{name: "double", setNonDef: func(m *dynamicpb.Message) { m.Set(md.Fields().ByNumber(6), protoreflect.ValueOfFloat64(1.5)) }},
		{name: "string-list", setNonDef: func(m *dynamicpb.Message) {
			l := m.Mutable(md.Fields().ByNumber(7)).List()
			l.Append(protoreflect.ValueOfString("a"))
		}},
		{name: "string_map", setNonDef: func(m *dynamicpb.Message) {
			mp := m.Mutable(md.Fields().ByNumber(8)).Map()
			mp.Set(protoreflect.MapKey(protoreflect.ValueOfString("k")), protoreflect.ValueOfString("v"))
		}},
		{name: "string_map-empty-key", setNonDef: func(m *dynamicpb.Message) {
			mp := m.Mutable(md.Fields().ByNumber(8)).Map()
			mp.Set(protoreflect.MapKey(protoreflect.ValueOfString("")), protoreflect.ValueOfString("v"))
		}},
		{name: "string_map-empty-value", setNonDef: func(m *dynamicpb.Message) {
			mp := m.Mutable(md.Fields().ByNumber(8)).Map()
			mp.Set(protoreflect.MapKey(protoreflect.ValueOfString("k")), protoreflect.ValueOfString(""))
		}},
		{name: "value_map-empty-string", setNonDef: func(m *dynamicpb.Message) {
			mp := m.Mutable(md.Fields().ByNumber(9)).Map()
			mp.Set(protoreflect.MapKey(protoreflect.ValueOfString("k")), protoreflect.ValueOfMessage(structpb.NewStringValue("").ProtoReflect()))
		}},
	}

	base := newKV(false)
	hBase, err := HashMessage(base)
	if err != nil {
		t.Fatalf("hash base: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newKV(false)
			tc.setNonDef(m)
			h, err := HashMessage(m)
			if err != nil {
				t.Fatalf("hash nondefault: %v", err)
			}
			if string(h) == string(hBase) {
				t.Fatalf("non-default should change hash")
			}
		})
	}
}
