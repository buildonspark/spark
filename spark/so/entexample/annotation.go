package entexample

import (
	"entgo.io/ent/schema"
)

// Annotation holds the example/fixture configuration for fields.
type Annotation struct {
	// Default is the default value for this field when creating test fixtures.
	// Use Val() for basic types (hex strings auto-detected for bytes fields),
	// or TypedValue constructors like PublicKeyHex(), GoType(), Expr(), etc. for special cases.
	Default defaultValue
}

type defaultValue struct {
	Value any
}

// Name returns the annotation name.
func (Annotation) Name() string {
	return "EntExample"
}

// Default creates an annotation with a typed default value.
func Default(value any) Annotation {
	return Annotation{Default: defaultValue{Value: value}}
}

// Merge implements the schema.Merger interface.
func (a Annotation) Merge(other schema.Annotation) schema.Annotation {
	if o, ok := other.(Annotation); ok {
		// Override with the other annotation's values if they're set
		// TypedValue is always "set" (it's not a pointer), so just use it
		a.Default = o.Default
	}
	return a
}
