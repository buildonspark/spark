package entfieldlint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSchemaFile(t *testing.T) {
	// Create a temporary schema file
	tmpDir := t.TempDir()
	schemaContent := `package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type User struct {
	ent.Schema
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty(),
		field.String("email").
			Unique(),
		field.Int64("age").
			Optional(),
		field.String("old_field").
			Deprecated(),
		field.Bytes("data").
			Immutable(),
	}
}
`
	schemaPath := filepath.Join(tmpDir, "user.go")
	if err := os.WriteFile(schemaPath, []byte(schemaContent), 0o644); err != nil {
		t.Fatalf("failed to write schema file: %v", err)
	}

	schema, err := ParseSchemaFile(schemaPath)
	if err != nil {
		t.Fatalf("ParseSchemaFile failed: %v", err)
	}

	if schema == nil {
		t.Fatal("expected schema, got nil")
	}

	if schema.Name != "User" {
		t.Errorf("expected schema name 'User', got '%s'", schema.Name)
	}

	if len(schema.Fields) != 5 {
		t.Errorf("expected 5 fields, got %d", len(schema.Fields))
	}

	// Check field parsing
	fieldMap := make(map[string]Field)
	for _, f := range schema.Fields {
		fieldMap[f.FieldName] = f
	}

	// Check non-deprecated fields
	for _, name := range []string{"name", "email", "age", "data"} {
		f, ok := fieldMap[name]
		if !ok {
			t.Errorf("expected field '%s' to exist", name)
			continue
		}
		if f.Deprecated {
			t.Errorf("expected field '%s' to not be deprecated", name)
		}
	}

	// Check deprecated field
	oldField, ok := fieldMap["old_field"]
	if !ok {
		t.Fatal("expected field 'old_field' to exist")
	}
	if !oldField.Deprecated {
		t.Error("expected field 'old_field' to be deprecated")
	}
}

func TestParseSchemaDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple schema files
	userSchema := `package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type User struct {
	ent.Schema
}

func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("name"),
		field.String("email"),
	}
}
`
	postSchema := `package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type Post struct {
	ent.Schema
}

func (Post) Fields() []ent.Field {
	return []ent.Field{
		field.String("title"),
		field.String("body"),
		field.String("old_column").Deprecated(),
	}
}
`

	if err := os.WriteFile(filepath.Join(tmpDir, "user.go"), []byte(userSchema), 0o644); err != nil {
		t.Fatalf("failed to write user schema: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "post.go"), []byte(postSchema), 0o644); err != nil {
		t.Fatalf("failed to write post schema: %v", err)
	}

	schemas, err := ParseSchemaDir(tmpDir)
	if err != nil {
		t.Fatalf("ParseSchemaDir failed: %v", err)
	}

	if len(schemas) != 2 {
		t.Errorf("expected 2 schemas, got %d", len(schemas))
	}

	// Build a map for easier testing
	schemaMap := make(map[string]Schema)
	for _, s := range schemas {
		schemaMap[s.Name] = s
	}

	user, ok := schemaMap["User"]
	if !ok {
		t.Fatal("expected User schema")
	}
	if len(user.Fields) != 2 {
		t.Errorf("expected 2 fields in User, got %d", len(user.Fields))
	}

	post, ok := schemaMap["Post"]
	if !ok {
		t.Fatal("expected Post schema")
	}
	if len(post.Fields) != 3 {
		t.Errorf("expected 3 fields in Post, got %d", len(post.Fields))
	}

	// Check deprecated field in Post
	for _, f := range post.Fields {
		if f.FieldName == "old_column" && !f.Deprecated {
			t.Error("expected 'old_column' to be deprecated")
		}
	}
}

func TestParseSchemaFile_AllFieldTypes(t *testing.T) {
	tmpDir := t.TempDir()
	schemaContent := `package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type AllTypes struct {
	ent.Schema
}

func (AllTypes) Fields() []ent.Field {
	return []ent.Field{
		field.String("str_field"),
		field.Int("int_field"),
		field.Int64("int64_field"),
		field.Uint64("uint64_field"),
		field.Float("float_field"),
		field.Bool("bool_field"),
		field.Time("time_field"),
		field.Bytes("bytes_field"),
		field.UUID("uuid_field", uuid.UUID{}),
		field.JSON("json_field", map[string]any{}),
		field.Enum("enum_field").Values("a", "b"),
	}
}
`
	schemaPath := filepath.Join(tmpDir, "all_types.go")
	if err := os.WriteFile(schemaPath, []byte(schemaContent), 0o644); err != nil {
		t.Fatalf("failed to write schema file: %v", err)
	}

	schema, err := ParseSchemaFile(schemaPath)
	if err != nil {
		t.Fatalf("ParseSchemaFile failed: %v", err)
	}

	expectedFields := []string{
		"str_field", "int_field", "int64_field", "uint64_field",
		"float_field", "bool_field", "time_field", "bytes_field",
		"uuid_field", "json_field", "enum_field",
	}

	if len(schema.Fields) != len(expectedFields) {
		t.Errorf("expected %d fields, got %d", len(expectedFields), len(schema.Fields))
	}

	fieldNames := make(map[string]bool)
	for _, f := range schema.Fields {
		fieldNames[f.FieldName] = true
	}

	for _, expected := range expectedFields {
		if !fieldNames[expected] {
			t.Errorf("expected field '%s' to be parsed", expected)
		}
	}
}

func TestFieldKey(t *testing.T) {
	f := Field{
		SchemaName: "User",
		FieldName:  "email",
	}

	expected := "User.email"
	if f.FieldKey() != expected {
		t.Errorf("expected FieldKey() = '%s', got '%s'", expected, f.FieldKey())
	}
}

func TestParseSchemaFile_NonSchema(t *testing.T) {
	tmpDir := t.TempDir()

	// A Go file that's not an Ent schema
	nonSchemaContent := `package schema

type Helper struct {
	Name string
}

func DoSomething() {}
`
	path := filepath.Join(tmpDir, "helper.go")
	if err := os.WriteFile(path, []byte(nonSchemaContent), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	schema, err := ParseSchemaFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if schema != nil {
		t.Error("expected nil schema for non-schema file")
	}
}
