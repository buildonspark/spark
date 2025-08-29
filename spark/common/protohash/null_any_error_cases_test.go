package protohash

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestNullAndAnyErrorCases(t *testing.T) {

	t.Run("Value_null_errors", func(t *testing.T) {
		v := structpb.NewNullValue()
		_, err := HashMessage(v.ProtoReflect())
		if err == nil {
			t.Fatalf("Expected error when hashing null Value, got nil")
		}
		if !strings.Contains(err.Error(), "cannot hash nil value") && !strings.Contains(err.Error(), "top-level scalar/value types are not hashable") {
			t.Fatalf("Unexpected error: %v", err)
		}
	})

	t.Run("Value_empty_errors", func(t *testing.T) {
		v := &structpb.Value{}
		_, err := HashMessage(v.ProtoReflect())
		if err == nil {
			t.Fatalf("Expected error when hashing empty Value, got nil")
		}
		if !strings.Contains(err.Error(), "invalid struct value") && !strings.Contains(err.Error(), "top-level scalar/value types are not hashable") {
			t.Fatalf("Unexpected error: %v", err)
		}
	})

	t.Run("ListValue_with_null_element_errors", func(t *testing.T) {
		lv := &structpb.ListValue{Values: []*structpb.Value{structpb.NewNullValue()}}
		_, err := HashMessage(lv.ProtoReflect())
		if err == nil {
			t.Fatalf("Expected error when hashing ListValue containing null, got nil")
		}
		// Accept either top-level wrapper error or inner list item hashing error
		if !strings.Contains(err.Error(), "hashing list item 0") && !strings.Contains(err.Error(), "top-level scalar/value types are not hashable") {
			t.Fatalf("Unexpected error: %v", err)
		}
	})

	t.Run("Struct_with_null_field_skipped", func(t *testing.T) {
		st := &structpb.Struct{Fields: map[string]*structpb.Value{
			"k": structpb.NewNullValue(),
		}}
		empty := &structpb.Struct{Fields: map[string]*structpb.Value{}}

		h1, err := HashMessage(st.ProtoReflect())
		if err != nil {
			t.Fatalf("Unexpected error hashing Struct with null field: %v", err)
		}
		h2, err := HashMessage(empty.ProtoReflect())
		if err != nil {
			t.Fatalf("Unexpected error hashing empty Struct: %v", err)
		}
		if string(h1) != string(h2) {
			t.Fatalf("Struct with null field should hash equal to empty struct")
		}
	})

	t.Run("Any_always_errors", func(t *testing.T) {
		a := &anypb.Any{TypeUrl: "type.googleapis.com/example.Message"}
		_, err := HashMessage(a.ProtoReflect())
		if err == nil {
			t.Fatalf("Expected error when hashing Any, got nil")
		}
		if !strings.Contains(err.Error(), "does not support hashing of Any type") {
			t.Fatalf("Unexpected error: %v", err)
		}
	})
}

func TestTopLevelScalarOrValueTypesError(t *testing.T) {

	t.Run("StructValue_false_errors", func(t *testing.T) {
		v := structpb.NewBoolValue(false)
		if _, err := HashMessage(v.ProtoReflect()); err == nil {
			t.Fatalf("Expected error hashing top-level structpb.Value(false)")
		}
	})

	t.Run("StructValue_zero_number_errors", func(t *testing.T) {
		v := structpb.NewNumberValue(0)
		if _, err := HashMessage(v.ProtoReflect()); err == nil {
			t.Fatalf("Expected error hashing top-level structpb.Value(0)")
		}
	})

	t.Run("StructValue_empty_string_errors", func(t *testing.T) {
		v := structpb.NewStringValue("")
		if _, err := HashMessage(v.ProtoReflect()); err == nil {
			t.Fatalf("Expected error hashing top-level structpb.Value(\"\")")
		}
	})

	t.Run("Wrapper_false_bool_errors", func(t *testing.T) {
		w := &wrapperspb.BoolValue{Value: false}
		if _, err := HashMessage(w.ProtoReflect()); err == nil {
			t.Fatalf("Expected error hashing top-level BoolValue(false)")
		}
	})

	t.Run("Wrapper_zero_int64_errors", func(t *testing.T) {
		w := &wrapperspb.Int64Value{Value: 0}
		if _, err := HashMessage(w.ProtoReflect()); err == nil {
			t.Fatalf("Expected error hashing top-level Int64Value(0)")
		}
	})

	t.Run("Wrapper_zero_double_errors", func(t *testing.T) {
		w := &wrapperspb.DoubleValue{Value: 0}
		if _, err := HashMessage(w.ProtoReflect()); err == nil {
			t.Fatalf("Expected error hashing top-level DoubleValue(0)")
		}
	})
}
