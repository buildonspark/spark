package entexample

import (
	"embed"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"time"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
	"entgo.io/ent/schema/field"
)

//go:embed templates/*
var templates embed.FS

// typeRegistry maps type Ident (fully qualified type name) to custom rendering functions.
// This allows types to define custom rendering without requiring the actual type at codegen time.
var typeRegistry = map[string]func(any) string{
	"keys.Public": func(v any) string {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`keys.MustParsePublicKeyHex(%q)`, s)
		}
		return fmt.Sprintf(`keys.MustParsePublicKeyHex(%q)`, v)
	},
	"keys.Private": func(v any) string {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`keys.MustParsePrivateKeyHex(%q)`, s)
		}
		return fmt.Sprintf(`keys.MustParsePrivateKeyHex(%q)`, v)
	},
	"frost.SigningCommitment": func(v any) string {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`frost.MustParseSigningCommitment(%q)`, s)
		}
		return fmt.Sprintf(`frost.MustParseSigningCommitment(%q)`, v)
	},
	"frost.SigningNonce": func(v any) string {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`frost.MustParseSigningNonce(%q)`, s)
		}
		return fmt.Sprintf(`frost.MustParseSigningNonce(%q)`, v)
	},
	"schematype.TxID": func(v any) string {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`schematype.MustParseTxID(%q)`, s)
		}
		return fmt.Sprintf(`schematype.MustParseTxID(%q)`, v)
	},
	"uint128.Uint128": func(v any) string {
		if f, ok := v.(float64); ok {
			return fmt.Sprintf("uint128.NewFromUint64(uint64(%d))", uint64(f))
		}
		return fmt.Sprintf("uint128.NewFromUint64(uint64(%v))", v)
	},
	"uuid.UUID": func(v any) string {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`uuid.MustParse(%q)`, s)
		}
		return fmt.Sprintf(`uuid.MustParse(%q)`, v)
	},
}

// Extension is the test builder extension.
type Extension struct {
	entc.DefaultExtension
}

// Templates returns the templates for the extension.
func (e *Extension) Templates() []*gen.Template {
	tmpl := gen.NewTemplate("entexample/entexample.go").
		Funcs(gen.Funcs).
		Funcs(map[string]any{
			"formatDefault": func(field *gen.Field, ann any) string {
				// Ent serializes annotations as map[string]interface{}
				if m, ok := ann.(map[string]any); ok {
					// Default is a defaultValue struct with a "Value" field
					if defaultValStruct, ok := m["Default"].(map[string]any); ok {
						if value, ok := defaultValStruct["Value"]; ok {
							return renderValueForField(field, value)
						} else {
							return renderValueForField(field, nil)
						}
					}
				}
				return ""
			},
		})

	return []*gen.Template{
		gen.MustParse(tmpl.ParseFS(templates, "templates/*.tmpl")),
	}
}

// renderWithFieldType renders a value based on the Ent field type.
func renderValueForField(f *gen.Field, value any) string {
	// Check if the field's Go type has a custom renderer registered
	if f.Type.RType != nil {
		if renderFunc, ok := typeRegistry[f.Type.Ident]; ok {
			return renderFunc(value)
		}
	}

	// Handle basic types using type constants
	switch f.Type.Type {
	case field.TypeString:
		if s, ok := value.(string); ok {
			return fmt.Sprintf("%q", s)
		}
		return fmt.Sprintf("%q", value)

	case field.TypeBool:
		return fmt.Sprintf("%v", value)

	case field.TypeInt:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("int(%d)", int(f))
		}
		return fmt.Sprintf("int(%v)", value)

	case field.TypeInt8:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("int8(%d)", int8(f))
		}
		return fmt.Sprintf("int8(%v)", value)

	case field.TypeInt16:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("int16(%d)", int16(f))
		}
		return fmt.Sprintf("int16(%v)", value)

	case field.TypeInt32:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("int32(%d)", int32(f))
		}
		return fmt.Sprintf("int32(%v)", value)

	case field.TypeInt64:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("int64(%d)", int64(f))
		}
		return fmt.Sprintf("int64(%v)", value)

	case field.TypeUint:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("uint(%d)", uint(f))
		}
		return fmt.Sprintf("uint(%v)", value)

	case field.TypeUint8:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("uint8(%d)", uint8(f))
		}
		return fmt.Sprintf("uint8(%v)", value)

	case field.TypeUint16:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("uint16(%d)", uint16(f))
		}
		return fmt.Sprintf("uint16(%v)", value)

	case field.TypeUint32:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("uint32(%d)", uint32(f))
		}
		return fmt.Sprintf("uint32(%v)", value)

	case field.TypeUint64:
		if f, ok := value.(float64); ok {
			return fmt.Sprintf("uint64(%d)", uint64(f))
		}
		return fmt.Sprintf("uint64(%v)", value)

	case field.TypeTime:
		if s, ok := value.(string); ok {
			// Ensure the time we are generated is always in UTC.
			t, err := time.Parse(time.RFC3339, s)
			if err == nil {
				return fmt.Sprintf("func() time.Time { t, _ := time.Parse(time.RFC3339, %q); return t }()", t.UTC().Format(time.RFC3339))
			}
		}
		return fmt.Sprintf("%#v", value)

	case field.TypeBytes:
		// Handle byte slices - accept hex strings and render them properly
		if s, ok := value.(string); ok {
			// Try to decode as hex - if it's valid hex, use hex.DecodeString
			if _, err := hex.DecodeString(s); err == nil && s != "" {
				// Valid hex string - render as hex.DecodeString
				return fmt.Sprintf(`func() []byte { b, _ := hex.DecodeString(%q); return b }()`, s)
			}
			// Not hex, render as plain string literal
			return fmt.Sprintf("[]byte(%q)", s)
		}
		return fmt.Sprintf("[]byte(%q)", value)

	case field.TypeJSON:
		// Check for map types
		if f.Type.RType != nil && f.Type.RType.Kind == reflect.Map {
			if f.Type.RType.Ident == "map[string][]uint8" {
				// Handle map[string][]byte where values are hex strings
				if m, ok := value.(map[string]any); ok {
					// Sort keys for deterministic output
					keys := make([]string, 0, len(m))
					for k := range m {
						keys = append(keys, k)
					}
					sort.Strings(keys)

					result := "map[string][]byte{"
					first := true
					for _, k := range keys {
						v := m[k]
						if !first {
							result += ", "
						}
						first = false
						// Expect v to be a hex string
						if s, ok := v.(string); ok {
							result += fmt.Sprintf("%q: func() []byte { b, _ := hex.DecodeString(%q); return b }()", k, s)
						} else {
							result += fmt.Sprintf("%q: []byte(%q)", k, v)
						}
					}
					result += "}"
					return result
				}
			}
			if f.Type.RType.Ident == "map[string]keys.Public" {
				// Handle map[string]keys.Public where values are hex strings
				if m, ok := value.(map[string]any); ok {
					// Sort keys for deterministic output
					keys := make([]string, 0, len(m))
					for k := range m {
						keys = append(keys, k)
					}
					sort.Strings(keys)

					result := "map[string]keys.Public{"
					first := true
					for _, k := range keys {
						v := m[k]
						if !first {
							result += ", "
						}
						first = false
						// Expect v to be a hex string
						if s, ok := v.(string); ok {
							result += fmt.Sprintf("%q: keys.MustParsePublicKeyHex(%q)", k, s)
						} else {
							result += fmt.Sprintf("%q: keys.MustParsePublicKeyHex(%q)", k, v)
						}
					}
					result += "}"
					return result
				}
			}
		}

		// Check for slice types
		if f.Type.RType != nil && f.Type.RType.Kind == reflect.Slice {
			if f.Type.RType.Ident == "[]string" {
				// Handle []string (field.Strings())
				// After JSON deserialization, slices come through as []any
				if slice, ok := value.([]any); ok {
					result := "[]string{"
					for i, v := range slice {
						if i > 0 {
							result += ", "
						}
						if s, ok := v.(string); ok {
							result += fmt.Sprintf("%q", s)
						} else {
							result += fmt.Sprintf("%q", v)
						}
					}
					result += "}"
					return result
				}
			}
		}

		// Other JSON types, use %#v
		return fmt.Sprintf("%#v", value)

	case field.TypeEnum:
		// For enums with custom GoType, use the fully qualified constant name
		// Value should be the enum constant (e.g., schematype.NetworkRegtest)
		return fmt.Sprintf("%#v", value)

	default:
		// For custom types (enums, GoTypes, etc.), use %#v
		return fmt.Sprintf("%#v", value)
	}
}
