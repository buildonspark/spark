// Command entfieldlint checks for Ent schema field removal without deprecation.
//
// It compares the current schema against a base revision, so it needs whichever version-control system tracks the
// checkout. It supports both git working trees and jj workspaces.
//
// Usage:
//
//	entfieldlint check --base=HEAD^ --schema-dir=spark/so/ent/schema
//	entfieldlint list --schema-dir=spark/so/ent/schema
//	entfieldlint diff --base=HEAD^ --schema-dir=spark/so/ent/schema
package main

import (
	"archive/tar"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/lightsparkdev/spark/tools/entfieldlint"
)

// materializeConcurrency bounds the per-file subprocesses the jj backend overlaps.
const materializeConcurrency = 16

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
		_, _ = fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`entfieldlint - Check Ent schema field deprecation before removal

Commands:
  check    Check for fields removed without deprecation (compares against base ref)
  list     List all fields in the current schema
  diff     Show difference between base ref and current schema

Flags:
  --base         Revision to compare against (default: HEAD^ for git, @- for jj)
  --schema-dir   Path to ent/schema directory relative to repo root (default: spark/so/ent/schema)
  --json         Output in JSON format`)
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	baseRef := fs.String("base", "", "Revision to compare against (default: HEAD^ for git, @- for jj)")
	schemaDir := fs.String("schema-dir", "spark/so/ent/schema", "Path to ent/schema directory relative to repo root")
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	vc, err := detectVCS()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if *baseRef == "" {
		*baseRef = vc.defaultBase()
	}

	// Parse current schema
	currentSchemaPath := filepath.Join(vc.root(), *schemaDir)
	currentSchemas, err := entfieldlint.ParseSchemaDir(currentSchemaPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error parsing current schema: %v\n", err)
		return 1
	}

	// Parse base schema from the base revision
	baseSchemas, err := parseSchemasFromRef(vc, *baseRef, *schemaDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error parsing base schema: %v\n", err)
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

	sortByField(violations, Violation.sortKey)

	if len(violations) == 0 {
		if !*jsonOutput {
			fmt.Println("✓ No field removal violations found")
		}
		return 0
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(violations, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("✗ Found %d field removal violation(s):\n\n", len(violations))
		for _, v := range violations {
			fmt.Printf("  • %s\n", v.Message)
			fmt.Printf("    To fix: Add .Deprecated() to the field in %s schema, merge that change,\n", v.SchemaName)
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

	v, err := detectVCS()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	schemaPath := filepath.Join(v.root(), *schemaDir)
	schemas, err := entfieldlint.ParseSchemaDir(schemaPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error parsing schema: %v\n", err)
		return 1
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(schemas, "", "  ")
		fmt.Println(string(data))
		return 0
	}

	for _, schema := range schemas {
		fmt.Printf("%s:\n", schema.Name)
		for _, field := range schema.Fields {
			deprecated := ""
			if field.Deprecated {
				deprecated = " [DEPRECATED]"
			}
			fmt.Printf("  - %s%s\n", field.FieldName, deprecated)
		}
		fmt.Println()
	}

	return 0
}

func runDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	baseRef := fs.String("base", "", "Revision to compare against (default: HEAD^ for git, @- for jj)")
	schemaDir := fs.String("schema-dir", "spark/so/ent/schema", "Path to ent/schema directory relative to repo root")
	jsonOutput := fs.Bool("json", false, "Output in JSON format")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	v, err := detectVCS()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	repoRoot := v.root()
	if *baseRef == "" {
		*baseRef = v.defaultBase()
	}

	currentSchemaPath := filepath.Join(repoRoot, *schemaDir)
	currentSchemas, err := entfieldlint.ParseSchemaDir(currentSchemaPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error parsing current schema: %v\n", err)
		return 1
	}

	baseSchemas, err := parseSchemasFromRef(v, *baseRef, *schemaDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error parsing base schema: %v\n", err)
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

	sortByField(diff.Added, FieldInfo.sortKey)
	sortByField(diff.Removed, FieldInfo.sortKey)
	sortByField(diff.DeprecationAdded, FieldInfo.sortKey)

	if *jsonOutput {
		data, _ := json.MarshalIndent(diff, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	if len(diff.Added) > 0 {
		fmt.Println("Added fields:")
		for _, f := range diff.Added {
			fmt.Printf("  + %s.%s\n", f.SchemaName, f.FieldName)
		}
		fmt.Println()
	}
	if len(diff.DeprecationAdded) > 0 {
		fmt.Println("Newly deprecated fields:")
		for _, f := range diff.DeprecationAdded {
			fmt.Printf("  ~ %s.%s\n", f.SchemaName, f.FieldName)
		}
		fmt.Println()
	}
	if len(diff.Removed) > 0 {
		fmt.Println("Removed fields:")
		for _, f := range diff.Removed {
			status := "✓ was deprecated"
			if f.RemovedWithoutDep {
				status = "✗ NOT deprecated first"
			}
			fmt.Printf("  - %s.%s (%s)\n", f.SchemaName, f.FieldName, status)
		}
		fmt.Println()
	}
	if len(diff.Added) == 0 && len(diff.Removed) == 0 && len(diff.DeprecationAdded) == 0 {
		fmt.Println("No schema field changes detected")
	}
	return 0
}

// sortByField orders results by schema name then field name. Both commands collect their results by ranging over a
// map, so without this the output order — and for check, which findings a truncated log shows — varies run to run.
func sortByField[T any](items []T, key func(T) (string, string)) {
	slices.SortFunc(items, func(a, b T) int {
		aSchema, aField := key(a)
		bSchema, bField := key(b)
		return cmp.Or(strings.Compare(aSchema, bSchema), strings.Compare(aField, bField))
	})
}

type Violation struct {
	SchemaName string `json:"schema_name"`
	FieldName  string `json:"field_name"`
	Message    string `json:"message"`
}

func (v Violation) sortKey() (string, string) { return v.SchemaName, v.FieldName }

type FieldInfo struct {
	SchemaName        string `json:"schema_name"`
	FieldName         string `json:"field_name"`
	Deprecated        bool   `json:"deprecated"`
	RemovedWithoutDep bool   `json:"removed_without_deprecation,omitempty"`
}

func (f FieldInfo) sortKey() (string, string) { return f.SchemaName, f.FieldName }

type SchemaDiff struct {
	Added            []FieldInfo `json:"added"`
	Removed          []FieldInfo `json:"removed"`
	DeprecationAdded []FieldInfo `json:"deprecation_added"`
}

// vcs abstracts the version-control operations entfieldlint needs: locating the repo root and reading schema files at a
// base revision. It's implemented for both git and jj so the tool works in a plain git checkout and in a (possibly
// non-colocated) jj workspace, where there is no .git and git commands fail.
type vcs interface {
	// root returns the absolute path of the repository/workspace root. schemaDir is relative to it.
	root() string
	// materialize writes the schema files under schemaDir at ref into destDir, returning the directory that
	// directly contains them. It is deliberately bulk rather than per-file: reading 50+ files one subprocess at a
	// time costs more than an order of magnitude more than the parse it feeds.
	materialize(ref, schemaDir, destDir string) (string, error)
	// defaultBase is the revision compared against when --base is not supplied.
	defaultBase() string
}

// detectVCS picks git if the current directory is a git working tree, otherwise jj. git is tried first so behavior is
// unchanged wherever git already works (CI, colocated repos); jj is the fallback for non-colocated workspaces.
func detectVCS() (vcs, error) {
	if root, err := runOutput("", "git", "rev-parse", "--show-toplevel"); err == nil {
		return &gitVCS{repoRoot: strings.TrimSpace(root)}, nil
	}
	if root, err := runOutput("", "jj", "root", "--ignore-working-copy"); err == nil {
		return &jjVCS{repoRoot: strings.TrimSpace(root)}, nil
	}
	return nil, fmt.Errorf("not inside a git repository or jj workspace")
}

type gitVCS struct{ repoRoot string }

func (g *gitVCS) root() string        { return g.repoRoot }
func (g *gitVCS) defaultBase() string { return "HEAD^" }

// materialize streams the whole schema directory out of the object store in a single `git archive` and unpacks it in
// process, so cost is one subprocess regardless of how many schema files exist.
func (g *gitVCS) materialize(ref, schemaDir, destDir string) (string, error) {
	cmd := exec.Command("git", "archive", "--format=tar", ref, "--", schemaDir)
	cmd.Dir = g.repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", withStderr(err)
	}
	if err := extractTar(out, destDir); err != nil {
		return "", err
	}
	// git archive keeps the full path of each entry, so the files land under destDir/schemaDir. Nested
	// subdirectories come along too but are ignored by ParseSchemaDir, matching how the working tree is parsed.
	return filepath.Join(destDir, schemaDir), nil
}

type jjVCS struct{ repoRoot string }

func (j *jjVCS) root() string { return j.repoRoot }

// defaultBase is @-, the parent of the working-copy commit — the jj analog of git's HEAD^.
func (j *jjVCS) defaultBase() string { return "@-" }

func (j *jjVCS) listSchemaFiles(ref, schemaDir string) ([]string, error) {
	// --ignore-working-copy keeps this read-only: jj otherwise snapshots the working copy on every command.
	out, err := runOutput(j.repoRoot, "jj", "file", "list", "--ignore-working-copy", "-r", ref, schemaDir)
	if err != nil {
		return nil, err
	}
	// jj file list recurses, but the base revision has to be parsed the same way as the working tree, and
	// ParseSchemaDir only looks at the top level. Keeping a nested schema here would surface it at the base but
	// not in the working tree, reporting every one of its fields as removed.
	var files []string
	for _, file := range goFiles(out) {
		if filepath.Dir(file) == filepath.Clean(schemaDir) {
			files = append(files, file)
		}
	}
	return files, nil
}

// materialize fetches each schema file concurrently. jj has no bulk equivalent of `git archive` — `jj file show` with
// several paths concatenates their contents with no separator, so the files can't be told apart — which leaves
// overlapping the per-file subprocesses as the way to keep this off the critical path. The files all sit directly
// under schemaDir, so writing them out by base name can't collide.
func (j *jjVCS) materialize(ref, schemaDir, destDir string) (string, error) {
	files, err := j.listSchemaFiles(ref, schemaDir)
	if err != nil {
		return "", err
	}

	outDir := filepath.Join(destDir, schemaDir)
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}

	var g errgroup.Group
	g.SetLimit(materializeConcurrency)
	for _, file := range files {
		g.Go(func() error {
			cmd := exec.Command("jj", "file", "show", "--ignore-working-copy", "-r", ref, file)
			cmd.Dir = j.repoRoot
			content, err := cmd.Output()
			if err != nil {
				return nil // Skip files that don't exist at this revision.
			}
			return os.WriteFile(filepath.Join(outDir, filepath.Base(file)), content, 0o600)
		})
	}
	if err := g.Wait(); err != nil {
		return "", err
	}
	return outDir, nil
}

// withStderr folds a failed command's stderr into its error. Without it the caller reports a bare "exit status 128",
// which hides the usual cause: the schema directory not existing at that revision.
func withStderr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(exitErr.Stderr))
	}
	return err
}

// runOutput runs a command in dir (or the current directory if dir is empty) and returns its stdout.
func runOutput(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// goFiles splits newline-separated command output into the .go paths it contains.
func goFiles(out string) []string {
	var files []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if strings.HasSuffix(line, ".go") {
			files = append(files, line)
		}
	}
	return files
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

func parseSchemasFromRef(v vcs, ref, schemaDir string) ([]entfieldlint.Schema, error) {
	tmpDir, err := os.MkdirTemp("", "entfieldlint-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	schemaPath, err := v.materialize(ref, schemaDir, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s at %s: %w", schemaDir, ref, err)
	}

	schemas, err := entfieldlint.ParseSchemaDir(schemaPath)
	if err != nil {
		return nil, err
	}
	if len(schemas) == 0 {
		return nil, fmt.Errorf("no schema files found in %s at ref %s", schemaDir, ref)
	}
	return schemas, nil
}

// extractTar unpacks a tar stream into destDir, keeping only regular files.
func extractTar(data []byte, destDir string) error {
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		// Reject anything that isn't a plain relative path inside the archive. git archive never produces one,
		// but unpacking whatever a subprocess hands us without checking is how path traversal bugs happen.
		if !filepath.IsLocal(hdr.Name) {
			return fmt.Errorf("tar entry is not a local path: %s", hdr.Name)
		}
		target := filepath.Join(destDir, hdr.Name)

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
}
