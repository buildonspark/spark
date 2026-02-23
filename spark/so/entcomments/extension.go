package entcomments

import (
	"fmt"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

// Extension validates that all fields have comments.
type Extension struct {
	entc.DefaultExtension
}

// Hooks returns hooks for the extension.
func (e *Extension) Hooks() []gen.Hook {
	return []gen.Hook{
		validateFieldComments(),
	}
}

// validateFieldComments validates that all fields have comments.
func validateFieldComments() gen.Hook {
	return func(next gen.Generator) gen.Generator {
		return gen.GenerateFunc(func(g *gen.Graph) error {
			for _, node := range g.Nodes {
				for _, field := range node.Fields {
					// Skip the ID field since ent auto-generates it with a comment
					if field.Name == "id" {
						continue
					}

					// Check if the field has a comment
					if field.Comment() == "" {
						return fmt.Errorf(
							"schema %q: field %q is missing a .Comment() call. "+
								"All fields must have documentation. "+
								"Add .Comment(\"description\") to the field definition in the appropriate schema file",
							node.Name, field.Name,
						)
					}
				}
			}
			return next.Generate(g)
		})
	}
}
