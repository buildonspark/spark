// Command entfieldlint checks for Ent schema field removal without deprecation.
//
// Usage:
//
//	entfieldlint check --base=HEAD^ --schema-dir=spark/so/ent/schema
//	entfieldlint list --schema-dir=spark/so/ent/schema
//	entfieldlint diff --base=HEAD^ --schema-dir=spark/so/ent/schema
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lightsparkdev/spark/tools/entfieldlint"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "check":
		os.Exit(runCheck(os.Args[2:]))
	case "list":
		os.Exit(runList(os.Args[2:]))
	case "diff":
		os.Exit(runDiff(os.Args[2:]))
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	//nolint:forbidigo // CLI tool needs to print to stdout
	fmt.Println(`entfieldlint - Check Ent schema field deprecation before removal

Commands:
  check    Check for fields removed without deprecation (compares against base ref)
  list     List all fields in the current schema
  diff     Show difference between base ref and current schema

Flags:
  --base         Git ref to compare against (default: HEAD^)
  --schema-dir   Path to ent/schema directory relative to repo root (default: spark/so/ent/schema)
  --json         Output in JSON format`)
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	baseRef := fs.String("base", "HEAD^", "Git ref to compare against")
	schemaDir := fs.String("schema-dir", "spark/so/ent/schema", "Path to ent/schema directory relative to repo root")
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	// Get repo root
	repoRoot, err := getRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: not in a git repository: %v\n", err)
		return 1
	}

	// Parse current schema
	currentSchemaPath := filepath.Join(repoRoot, *schemaDir)
	currentSchemas, err := entfieldlint.ParseSchemaDir(currentSchemaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing current schema: %v\n", err)
		return 1
	}

	// Parse base schema from git ref
	baseSchemas, err := parseSchemasFromRef(*baseRef, *schemaDir, repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing base schema: %v\n", err)
		return 1
	}

	// Build field maps
	currentFields := buildFieldMap(currentSchemas)
	baseFields := buildFieldMap(baseSchemas)

	// Find removed fields that weren't deprecated
	var violations []Violation
	for key, baseField := range baseFields {
		if _, exists := currentFields[key]; !exists {
			if !baseField.Deprecated {
				violations = append(violations, Violation{
					SchemaName: baseField.SchemaName,
					FieldName:  baseField.FieldName,
					Message:    fmt.Sprintf("field %s.%s was removed without being deprecated first", baseField.SchemaName, baseField.FieldName),
				})
			}
		}
	}

	if len(violations) == 0 {
		if !*jsonOutput {
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println("✓ No field removal violations found")
		}
		return 0
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(violations, "", "  ")
		//nolint:forbidigo // CLI tool needs to print to stdout
		fmt.Println(string(data))
	} else {
		//nolint:forbidigo // CLI tool needs to print to stdout
		fmt.Printf("✗ Found %d field removal violation(s):\n\n", len(violations))
		for _, v := range violations {
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Printf("  • %s\n", v.Message)
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Printf("    To fix: Add .Deprecated() to the field in %s schema, merge that change,\n", v.SchemaName)
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Printf("    then remove the field in a follow-up PR.\n\n")
		}
	}

	return 1
}

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	schemaDir := fs.String("schema-dir", "spark/so/ent/schema", "Path to ent/schema directory relative to repo root")
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	repoRoot, err := getRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: not in a git repository: %v\n", err)
		return 1
	}

	schemaPath := filepath.Join(repoRoot, *schemaDir)
	schemas, err := entfieldlint.ParseSchemaDir(schemaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing schema: %v\n", err)
		return 1
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(schemas, "", "  ")
		//nolint:forbidigo // CLI tool needs to print to stdout
		fmt.Println(string(data))
	} else {
		for _, schema := range schemas {
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Printf("%s:\n", schema.Name)
			for _, field := range schema.Fields {
				deprecated := ""
				if field.Deprecated {
					deprecated = " [DEPRECATED]"
				}
				//nolint:forbidigo // CLI tool needs to print to stdout
				fmt.Printf("  - %s%s\n", field.FieldName, deprecated)
			}
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println()
		}
	}

	return 0
}

func runDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	baseRef := fs.String("base", "HEAD^", "Git ref to compare against")
	schemaDir := fs.String("schema-dir", "spark/so/ent/schema", "Path to ent/schema directory relative to repo root")
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	repoRoot, err := getRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: not in a git repository: %v\n", err)
		return 1
	}

	currentSchemaPath := filepath.Join(repoRoot, *schemaDir)
	currentSchemas, err := entfieldlint.ParseSchemaDir(currentSchemaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing current schema: %v\n", err)
		return 1
	}

	baseSchemas, err := parseSchemasFromRef(*baseRef, *schemaDir, repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing base schema: %v\n", err)
		return 1
	}

	currentFields := buildFieldMap(currentSchemas)
	baseFields := buildFieldMap(baseSchemas)

	diff := SchemaDiff{
		Added:            []FieldInfo{},
		Removed:          []FieldInfo{},
		DeprecationAdded: []FieldInfo{},
	}

	// Find added fields
	for key, field := range currentFields {
		if _, exists := baseFields[key]; !exists {
			diff.Added = append(diff.Added, FieldInfo{
				SchemaName: field.SchemaName,
				FieldName:  field.FieldName,
				Deprecated: field.Deprecated,
			})
		}
	}

	// Find removed fields and deprecation changes
	for key, baseField := range baseFields {
		currentField, exists := currentFields[key]
		if !exists {
			diff.Removed = append(diff.Removed, FieldInfo{
				SchemaName:        baseField.SchemaName,
				FieldName:         baseField.FieldName,
				Deprecated:        baseField.Deprecated,
				RemovedWithoutDep: !baseField.Deprecated,
			})
		} else if !baseField.Deprecated && currentField.Deprecated {
			diff.DeprecationAdded = append(diff.DeprecationAdded, FieldInfo{
				SchemaName: currentField.SchemaName,
				FieldName:  currentField.FieldName,
				Deprecated: true,
			})
		}
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(diff, "", "  ")
		//nolint:forbidigo // CLI tool needs to print to stdout
		fmt.Println(string(data))
	} else {
		if len(diff.Added) > 0 {
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println("Added fields:")
			for _, f := range diff.Added {
				//nolint:forbidigo // CLI tool needs to print to stdout
				fmt.Printf("  + %s.%s\n", f.SchemaName, f.FieldName)
			}
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println()
		}
		if len(diff.DeprecationAdded) > 0 {
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println("Newly deprecated fields:")
			for _, f := range diff.DeprecationAdded {
				//nolint:forbidigo // CLI tool needs to print to stdout
				fmt.Printf("  ~ %s.%s\n", f.SchemaName, f.FieldName)
			}
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println()
		}
		if len(diff.Removed) > 0 {
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println("Removed fields:")
			for _, f := range diff.Removed {
				status := "✓ was deprecated"
				if f.RemovedWithoutDep {
					status = "✗ NOT deprecated first"
				}
				//nolint:forbidigo // CLI tool needs to print to stdout
				fmt.Printf("  - %s.%s (%s)\n", f.SchemaName, f.FieldName, status)
			}
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println()
		}
		if len(diff.Added) == 0 && len(diff.Removed) == 0 && len(diff.DeprecationAdded) == 0 {
			//nolint:forbidigo // CLI tool needs to print to stdout
			fmt.Println("No schema field changes detected")
		}
	}

	return 0
}

type Violation struct {
	SchemaName string `json:"schema_name"`
	FieldName  string `json:"field_name"`
	Message    string `json:"message"`
}

type FieldInfo struct {
	SchemaName        string `json:"schema_name"`
	FieldName         string `json:"field_name"`
	Deprecated        bool   `json:"deprecated"`
	RemovedWithoutDep bool   `json:"removed_without_deprecation,omitempty"`
}

type SchemaDiff struct {
	Added            []FieldInfo `json:"added"`
	Removed          []FieldInfo `json:"removed"`
	DeprecationAdded []FieldInfo `json:"deprecation_added"`
}

func getRepoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func buildFieldMap(schemas []entfieldlint.Schema) map[string]entfieldlint.Field {
	fields := make(map[string]entfieldlint.Field)
	for _, schema := range schemas {
		for _, field := range schema.Fields {
			fields[field.FieldKey()] = field
		}
	}
	return fields
}

func parseSchemasFromRef(ref, schemaDir, repoRoot string) ([]entfieldlint.Schema, error) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "entfieldlint-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Get list of schema files from git
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", ref, schemaDir)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list files from %s: %w", ref, err)
	}

	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(files) == 0 || (len(files) == 1 && files[0] == "") {
		return nil, fmt.Errorf("no schema files found in %s at ref %s", schemaDir, ref)
	}

	// Extract each file
	for _, file := range files {
		if file == "" || !strings.HasSuffix(file, ".go") {
			continue
		}

		// Get file contents from git
		cmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", ref, file))
		cmd.Dir = repoRoot
		content, err := cmd.Output()
		if err != nil {
			continue // Skip files that don't exist
		}

		// Write to temp directory
		tmpFile := filepath.Join(tmpDir, filepath.Base(file))
		if err := os.WriteFile(tmpFile, content, 0o644); err != nil {
			return nil, fmt.Errorf("failed to write temp file: %w", err)
		}
	}

	// Parse the temp directory
	return entfieldlint.ParseSchemaDir(tmpDir)
}
