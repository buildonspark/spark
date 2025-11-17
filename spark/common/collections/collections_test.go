package collections

import (
	"testing"

	"google.golang.org/protobuf/testing/protocmp"

	"github.com/lightsparkdev/spark/proto/common"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
)

func TestMapOfArrayToArrayOfMap(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string][]int
		expected []map[string]int
	}{
		{
			name:     "normal case",
			input:    map[string][]int{"a": {1, 2}, "b": {3, 4}},
			expected: []map[string]int{{"a": 1, "b": 3}, {"a": 2, "b": 4}},
		},
		{
			name:     "single element arrays",
			input:    map[string][]int{"x": {1}, "y": {2}},
			expected: []map[string]int{{"x": 1, "y": 2}},
		},
		{
			name:     "multiple element arrays",
			input:    map[string][]int{"x": {1, 2, 3, 4, 5}, "y": {10, 11, 12, 13, 14}},
			expected: []map[string]int{{"x": 1, "y": 10}, {"x": 2, "y": 11}, {"x": 3, "y": 12}, {"x": 4, "y": 13}, {"x": 5, "y": 14}},
		},
		{
			name:     "empty map",
			input:    map[string][]int{},
			expected: []map[string]int{},
		},
		{
			name:     "uneven arrays",
			input:    map[string][]int{"a": {1, 2, 3}, "b": {4, 5}},
			expected: []map[string]int{{"a": 1, "b": 4}, {"a": 2, "b": 5}, {"a": 3}},
		},
		{
			name:     "empty arrays",
			input:    map[string][]int{"a": {}, "b": {}},
			expected: []map[string]int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MapOfArrayToArrayOfMap(tt.input)
			if diff := cmp.Diff(tt.expected, result); diff != "" {
				t.Errorf("MapOfArrayToArrayOfMap() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestArrayOfMapToMapOfArray(t *testing.T) {
	tests := []struct {
		name     string
		input    []map[string]int
		expected map[string][]int
	}{
		{
			name:     "normal case",
			input:    []map[string]int{{"a": 1, "b": 3}, {"a": 2, "b": 4}},
			expected: map[string][]int{"a": {1, 2}, "b": {3, 4}},
		},
		{
			name:     "single map",
			input:    []map[string]int{{"x": 1, "y": 2}},
			expected: map[string][]int{"x": {1}, "y": {2}},
		},
		{
			name:     "empty array",
			input:    []map[string]int{},
			expected: map[string][]int{},
		},
		{
			name:     "empty maps",
			input:    []map[string]int{{}, {}},
			expected: map[string][]int{},
		},
		{
			name:     "different keys in maps",
			input:    []map[string]int{{"a": 1}, {"b": 2}, {"c": 3}},
			expected: map[string][]int{"a": {1}, "b": {2}, "c": {3}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ArrayOfMapToMapOfArray(tt.input)
			if diff := cmp.Diff(tt.expected, result); diff != "" {
				t.Errorf("ArrayOfMapToMapOfArray() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSwapMapKeys(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]map[int]string
		want  map[int]map[string]string
	}{
		{
			name: "normal case",
			input: map[string]map[int]string{
				"a": {1: "b", 2: "c"},
				"d": {1: "e", 2: "f"},
			},
			want: map[int]map[string]string{
				1: {"a": "b", "d": "e"},
				2: {"a": "c", "d": "f"},
			},
		},
		{
			name:  "empty map",
			input: map[string]map[int]string{},
			want:  map[int]map[string]string{},
		},
		{
			name: "single key",
			input: map[string]map[int]string{
				"x": {1: "y", 2: "z"},
			},
			want: map[int]map[string]string{
				1: {"x": "y"},
				2: {"x": "z"},
			},
		},
		{
			name: "empty inner maps",
			input: map[string]map[int]string{
				"a": {},
				"b": {},
			},
			want: map[int]map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SwapMapKeys(tt.input)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("SwapMapKeys() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

type signingResult struct {
	signatureShare []byte
}

func (s signingResult) MarshalProto() (*common.SigningResult, error) {
	return &common.SigningResult{SignatureShare: s.signatureShare}, nil
}

func TestConvertObjectMapToProtoMap(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]*signingResult
		want  map[string]*common.SigningResult
	}{
		{
			name: "normal case",
			input: map[string]*signingResult{
				"key1": {signatureShare: []byte{1, 2, 3}},
				"key2": {signatureShare: []byte{4, 5, 6}},
			},
			want: map[string]*common.SigningResult{
				"key1": {SignatureShare: []byte{1, 2, 3}},
				"key2": {SignatureShare: []byte{4, 5, 6}},
			},
		},
		{
			name:  "empty map",
			input: map[string]*signingResult{},
			want:  map[string]*common.SigningResult{},
		},
		{
			name:  "single element",
			input: map[string]*signingResult{"single": {signatureShare: []byte{1, 2, 3, 4, 5}}},
			want:  map[string]*common.SigningResult{"single": {SignatureShare: []byte{1, 2, 3, 4, 5}}},
		},
		{
			name:  "empty signature share",
			input: map[string]*signingResult{"empty": {signatureShare: []byte{}}},
			want:  map[string]*common.SigningResult{"empty": {SignatureShare: []byte{}}},
		},
		{
			name:  "nil signature share",
			input: map[string]*signingResult{"nil": {signatureShare: nil}},
			want:  map[string]*common.SigningResult{"nil": {SignatureShare: nil}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConvertObjectMapToProtoMap(tt.input)
			if err != nil {
				t.Fatalf("ConvertObjectMapToProtoMap() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("ConvertObjectMapToProtoMap() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStringUUIDArrayToUUIDArray(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	validUUID2 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	tests := []struct {
		name  string
		input []string
		want  []uuid.UUID
	}{
		{
			name:  "valid UUIDs",
			input: []string{validUUID1, validUUID2},
			want:  []uuid.UUID{uuid.MustParse(validUUID1), uuid.MustParse(validUUID2)},
		},
		{
			name:  "empty array",
			input: []string{},
			want:  []uuid.UUID{},
		},
		{
			name:  "single valid UUID",
			input: []string{validUUID1},
			want:  []uuid.UUID{uuid.MustParse(validUUID1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := StringUUIDArrayToUUIDArray(tt.input)
			if err != nil {
				t.Fatalf("StringUUIDArrayToUUIDArray() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("StringUUIDArrayToUUIDArray() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStringUUIDArrayToUUIDArray_Errors(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	invalidUUID := "invalid-uuid"

	tests := []struct {
		name  string
		input []string
	}{
		{
			name:  "invalid UUID",
			input: []string{invalidUUID},
		},
		{
			name:  "mixed valid and invalid UUIDs",
			input: []string{validUUID1, invalidUUID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := StringUUIDArrayToUUIDArray(tt.input)
			if err == nil {
				t.Errorf("StringUUIDArrayToUUIDArray() want error but got none")
			}
			if got != nil {
				t.Errorf("StringUUIDArrayToUUIDArray() want nil but got %v", got)
			}
		})
	}
}
