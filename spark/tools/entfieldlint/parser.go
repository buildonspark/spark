// Package entfieldlint provides lint checks for Ent schema field deprecation.
package entfieldlint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Field represents an Ent schema field with its metadata.
type Field struct {
	SchemaName string
	FieldName  string
	Deprecated bool
	FilePath   string
	Line       int
}

// Schema represents an Ent schema with its fields.
type Schema struct {
	Name   string
	Fields []Field
}

// ParseSchemaDir parses all Ent schema files in a directory and returns the fields.
func ParseSchemaDir(dir string) ([]Schema, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var schemas []Schema
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		// Skip mixin.go and other non-schema files
		if entry.Name() == "mixin.go" || strings.HasPrefix(entry.Name(), "mixin") {
			continue
		}

		filePath := filepath.Join(dir, entry.Name())
		schema, err := ParseSchemaFile(filePath)
		if err != nil {
			// Skip files that don't parse as schemas
			continue
		}
		if schema != nil {
			schemas = append(schemas, *schema)
		}
	}

	return schemas, nil
}

// ParseSchemaFile parses a single Ent schema file and extracts field information.
func ParseSchemaFile(filePath string) (*Schema, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, parser.AllErrors)
	if err != nil {
		return nil, err
	}

	var schemaName string
	var fields []Field

	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.TypeSpec:
			// Find type declarations that embed ent.Schema
			if st, ok := x.Type.(*ast.StructType); ok {
				for _, f := range st.Fields.List {
					if sel, ok := f.Type.(*ast.SelectorExpr); ok {
						if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "ent" && sel.Sel.Name == "Schema" {
							schemaName = x.Name.Name
						}
					}
				}
			}

		case *ast.FuncDecl:
			// Find the Fields() method
			if x.Name.Name == "Fields" && x.Recv != nil && len(x.Recv.List) > 0 {
				// Extract fields from the return statement
				ast.Inspect(x.Body, func(n ast.Node) bool {
					if ret, ok := n.(*ast.ReturnStmt); ok {
						for _, result := range ret.Results {
							if comp, ok := result.(*ast.CompositeLit); ok {
								for _, elt := range comp.Elts {
									field := parseFieldExpr(elt, filePath, fset, schemaName)
									if field != nil {
										fields = append(fields, *field)
									}
								}
							}
						}
					}
					return true
				})
			}
		}
		return true
	})

	if schemaName == "" {
		return nil, nil
	}

	return &Schema{
		Name:   schemaName,
		Fields: fields,
	}, nil
}

// parseFieldExpr parses a field expression from an Ent schema and extracts metadata.
func parseFieldExpr(expr ast.Expr, filePath string, fset *token.FileSet, schemaName string) *Field {
	var fieldName string
	var deprecated bool
	var line int

	// Walk through the chained method calls
	walkFieldChain(expr, func(call *ast.CallExpr, methodName string) {
		switch methodName {
		case "String", "Int", "Int64", "Uint64", "Float", "Bool", "Time", "Bytes", "UUID", "JSON", "Enum", "Other":
			// This is a field type call - extract the field name
			if len(call.Args) > 0 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					fieldName = strings.Trim(lit.Value, `"`)
					line = fset.Position(call.Pos()).Line
				}
			}
		case "Deprecated":
			deprecated = true
		}
	})

	if fieldName == "" {
		return nil
	}

	return &Field{
		SchemaName: schemaName,
		FieldName:  fieldName,
		Deprecated: deprecated,
		FilePath:   filePath,
		Line:       line,
	}
}

// walkFieldChain walks through a chain of method calls and invokes the callback for each.
func walkFieldChain(expr ast.Expr, fn func(*ast.CallExpr, string)) {
	current := expr
	for {
		call, ok := current.(*ast.CallExpr)
		if !ok {
			return
		}

		// Get the method name
		var methodName string
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			methodName = fun.Sel.Name
			fn(call, methodName)
			current = fun.X
		case *ast.Ident:
			// This is the end of the chain (e.g., field.String)
			return
		default:
			return
		}
	}
}

// FieldKey returns a unique key for a field combining schema and field name.
func (f Field) FieldKey() string {
	return f.SchemaName + "." + f.FieldName
}
