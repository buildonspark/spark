package entexample

import (
	"embed"
	"encoding/hex"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
	"entgo.io/ent/schema/field"
	"k8s.io/apimachinery/pkg/api/validate/constraints"
)

//go:embed templates/*
var templates embed.FS

// typeRegistry maps type Ident (fully qualified type name) to custom rendering functions.
// This allows types to define custom rendering without requiring the actual type at codegen time.
var typeRegistry = map[string]func(any) (string, error){
	"keys.Public": func(v any) (string, error) {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`keys.MustParsePublicKeyHex(%q)`, s), nil
		}
		return "", fmt.Errorf("invalid value %#v (type %T) for keys.Public", v, v)
	},
	"keys.Private": func(v any) (string, error) {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`keys.MustParsePrivateKeyHex(%q)`, s), nil
		}
		return "", fmt.Errorf("invalid value %#v (type %T) for keys.Private", v, v)
	},
	"frost.SigningCommitment": func(v any) (string, error) {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`frost.MustParseSigningCommitment(%q)`, s), nil
		}
		return "", fmt.Errorf("invalid value %#v (type %T) for frost.SigningCommitment", v, v)
	},
	"frost.SigningNonce": func(v any) (string, error) {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`frost.MustParseSigningNonce(%q)`, s), nil
		}
		return "", fmt.Errorf("invalid value %#v (type %T) for frost.SigningNonce", v, v)
	},
	"schematype.TxID": func(v any) (string, error) {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`schematype.MustParseTxID(%q)`, s), nil
		}
		return "", fmt.Errorf("invalid value %#v (type %T) for schematype.TxID", v, v)
	},
	"uint128.Uint128": func(v any) (string, error) {
		if f, ok := v.(float64); ok {
			return fmt.Sprintf("uint128.FromUint64(uint64(%d))", uint64(f)), nil
		}
		return "", fmt.Errorf("invalid value %#v (type %T) for uint128.Uint128", v, v)
	},
	"uuid.UUID": func(v any) (string, error) {
		if s, ok := v.(string); ok {
			return fmt.Sprintf(`uuid.MustParse(%q)`, s), nil
		}
		return "", fmt.Errorf("invalid value %#v (type %T) for uuid.UUID", v, v)
	},
}

// Extension is the test builder extension.
type Extension struct {
	entc.DefaultExtension
}

// Hooks returns hooks for the extension.
func (e *Extension) Hooks() []gen.Hook {
	return []gen.Hook{
		validateEntExampleAnnotations(),
	}
}

// validateEntExampleAnnotations validates that all non-optional fields without Ent defaults
// have entexample.Default annotations.
func validateEntExampleAnnotations() gen.Hook {
	return func(next gen.Generator) gen.Generator {
		return gen.GenerateFunc(func(g *gen.Graph) error {
			for _, node := range g.Nodes {
				for _, field := range node.Fields {
					// Optional fields are allowed to not have examples.
					if field.Optional {
						continue
					}

					// Default fields can also not have examples, and we'll just use the default as the fixture
					// value.
					if field.Default {
						continue
					}

					hasEntExampleDefault := false
					if ann := field.Annotations; ann != nil {
						if annMap, ok := ann["EntExample"].(map[string]any); ok {
							if _, ok := annMap["Default"]; ok {
								hasEntExampleDefault = true
							}
						}
					}

					if !hasEntExampleDefault {
						return fmt.Errorf(
							"schema %q: field %q is required (non-optional) without an Ent default, but missing entexample.Default() annotation. "+
								"Add entexample.Default(value) to the field's Annotations() or make the field Optional()",
							node.Name, field.Name,
						)
					}
				}
			}
			return next.Generate(g)
		})
	}
}

// Templates returns the templates for the extension.
func (e *Extension) Templates() []*gen.Template {
	tmpl := gen.NewTemplate("entexample/entexample.go").
		Funcs(gen.Funcs).
		Funcs(map[string]any{
			"formatDefault": func(field *gen.Field, ann any) (string, error) {
				// Ent serializes annotations as map[string]interface{}
				if m, ok := ann.(map[string]any); ok {
					// Default is a defaultValue struct with a "Value" field
					if defaultValStruct, ok := m["Default"].(map[string]any); ok {
						if value, ok := defaultValStruct["Value"]; ok {
							return renderValueForField(field, value)
						}
						return renderValueForField(field, nil)
					}
				}
				return "", nil
			},
		})

	return []*gen.Template{
		gen.MustParse(tmpl.ParseFS(templates, "templates/*.tmpl")),
	}
}

// renderValueForField renders a value based on the Ent field type.
func renderValueForField(f *gen.Field, value any) (string, error) {
	// Check if the field's Go type has a custom renderer registered
	if f.Type.RType != nil {
		if renderFunc, ok := typeRegistry[f.Type.Ident]; ok {
			return renderFunc(value)
		}
	}

	// Handle basic types using type constants
	switch f.Type.Type {
	case field.TypeString, field.TypeBool:
		return fmt.Sprintf("%#v", value), nil
	case field.TypeInt:
		return truncateTo[int](value)
	case field.TypeInt8:
		return truncateTo[int8](value)
	case field.TypeInt16:
		return truncateTo[int16](value)
	case field.TypeInt32:
		return truncateTo[int32](value)
	case field.TypeInt64:
		return truncateTo[int64](value)
	case field.TypeUint:
		return truncateTo[uint](value)
	case field.TypeUint8:
		return truncateTo[uint8](value)
	case field.TypeUint16:
		return truncateTo[uint16](value)
	case field.TypeUint32:
		return truncateTo[uint32](value)
	case field.TypeUint64:
		return truncateTo[uint64](value)
	case field.TypeTime:
		if s, ok := value.(string); ok {
			// Ensure the time we are generated is always in UTC.
			t, err := time.Parse(time.RFC3339, s)
			if err == nil {
				return fmt.Sprintf("func() time.Time { t, _ := time.Parse(time.RFC3339, %q); return t }()", t.UTC().Format(time.RFC3339)), nil
			}
		}
		return fmt.Sprintf("%#v", value), nil

	case field.TypeBytes:
		// Handle byte slices - accept hex strings and render them properly
		if s, ok := value.(string); ok {
			// Try to decode as hex - if it's valid hex, use hex.DecodeString
			if _, err := hex.DecodeString(s); err == nil && s != "" {
				// Valid hex string - render as hex.DecodeString
				return fmt.Sprintf(`func() []byte { b, _ := hex.DecodeString(%q); return b }()`, s), nil
			}
			// Not hex, render as plain string literal
		}
		return fmt.Sprintf("[]byte(%q)", value), nil

	case field.TypeJSON:
		// Check for map types
		if f.Type.RType != nil && f.Type.RType.Kind == reflect.Map {
			if f.Type.RType.Ident == "map[string][]uint8" {
				// Handle map[string][]byte where values are hex strings
				if m, ok := value.(map[string]any); ok {
					// Sort keys for deterministic output
					keys := slices.Sorted(maps.Keys(m))

					result := &strings.Builder{}
					result.WriteString("map[string][]byte{")
					for _, k := range keys {
						v := m[k]
						// Expect v to be a hex string
						if s, ok := v.(string); ok {
							_, _ = fmt.Fprintf(result, "%q: func() []byte { b, _ := hex.DecodeString(%q); return b }(), ", k, s)
						} else {
							_, _ = fmt.Fprintf(result, "%q: []byte(%q), ", k, v)
						}
					}
					result.WriteRune('}')
					return result.String(), nil
				}
			}
			if f.Type.RType.Ident == "map[string]keys.Public" {
				// Handle map[string]keys.Public where values are hex strings
				if m, ok := value.(map[string]any); ok {
					// Sort keys for deterministic output
					keys := slices.Sorted(maps.Keys(m))

					result := &strings.Builder{}
					result.WriteString("map[string]keys.Public{")

					for _, k := range keys {
						_, _ = fmt.Fprintf(result, "%q: keys.MustParsePublicKeyHex(%q), ", k, m[k])
					}
					result.WriteRune('}')
					return result.String(), nil
				}
			}
		}

		// Check for slice types
		if f.Type.RType != nil && f.Type.RType.Kind == reflect.Slice {
			if f.Type.RType.Ident == "[]string" {
				// Handle []string (field.Strings())
				// After JSON deserialization, slices come through as []any
				if slice, ok := value.([]any); ok {
					result := &strings.Builder{}
					result.WriteString("[]string{")
					for _, v := range slice {
						_, _ = fmt.Fprintf(result, "%q, ", v)
					}
					result.WriteRune('}')
					return result.String(), nil
				}
			}
		}

		// Other JSON types, use %#v
		return fmt.Sprintf("%#v", value), nil

	case field.TypeEnum:
		// For enums with custom GoType, use the fully qualified constant name
		// Value should be the enum constant (e.g., schematype.NetworkRegtest)
		return fmt.Sprintf("%#v", value), nil

	default:
		// For custom types (enums, GoTypes, etc.), use %#v
		return fmt.Sprintf("%#v", value), nil
	}
}

func truncateTo[t constraints.Integer](a any) (string, error) {
	var zero t
	switch v := a.(type) {
	case float64:
		return fmt.Sprintf("%T(%d)", zero, t(v)), nil
	case t:
		return fmt.Sprintf("%T(%d)", zero, v), nil
	default:
		return "", fmt.Errorf("invalid value %#v (type %T) for %T", a, a, zero)
	}
}
