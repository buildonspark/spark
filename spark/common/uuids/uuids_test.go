package uuids

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseSlice(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	validUUID2 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	tests := []struct {
		name  string
		input []string
		want  uuid.UUIDs
	}{
		{
			name:  "valid UUIDs",
			input: []string{validUUID1, validUUID2},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1), uuid.MustParse(validUUID2)},
		},
		{
			name:  "empty array",
			input: []string{},
			want:  uuid.UUIDs{},
		},
		{
			name:  "single valid UUID",
			input: []string{validUUID1},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSlice(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseSlice_Errors(t *testing.T) {
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
			got, err := ParseSlice(tt.input)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

type testStruct struct {
	id string
}

func TestParseSliceFunc(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	validUUID2 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	tests := []struct {
		name  string
		input []testStruct
		want  uuid.UUIDs
	}{
		{
			name:  "valid UUIDs",
			input: []testStruct{{id: validUUID1}, {id: validUUID2}},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1), uuid.MustParse(validUUID2)},
		},
		{
			name:  "empty array",
			input: []testStruct{},
			want:  uuid.UUIDs{},
		},
		{
			name:  "single valid UUID",
			input: []testStruct{{id: validUUID1}},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSliceFunc(tt.input, func(s testStruct) string { return s.id })
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseSliceFunc_Errors(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	invalidUUID := "invalid-uuid"

	tests := []struct {
		name  string
		input []testStruct
	}{
		{
			name:  "invalid UUID",
			input: []testStruct{{id: invalidUUID}},
		},
		{
			name:  "mixed valid and invalid UUIDs",
			input: []testStruct{{id: validUUID1}, {id: invalidUUID}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSliceFunc(tt.input, func(s testStruct) string { return s.id })
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestParseSeq(t *testing.T) {
	validUUID1 := "550e8400-e29b-41d4-a716-446655440000"
	validUUID2 := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	tests := []struct {
		name  string
		input []string
		want  uuid.UUIDs
	}{
		{
			name:  "valid UUIDs",
			input: []string{validUUID1, validUUID2},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1), uuid.MustParse(validUUID2)},
		},
		{
			name:  "empty sequence",
			input: []string{},
			want:  uuid.UUIDs(nil),
		},
		{
			name:  "single valid UUID",
			input: []string{validUUID1},
			want:  uuid.UUIDs{uuid.MustParse(validUUID1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSeq(slices.Values(tt.input))
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseSeq_Errors(t *testing.T) {
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
			got, err := ParseSeq(slices.Values(tt.input))
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}
