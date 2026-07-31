package transfer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Nothing at runtime can enumerate a package's vars, so the sentinels are read back out of the
// source. Names come from the parse and values from the registry, so they meet on message text.
func TestAllManifestRefusalsCoversEveryDeclaredSentinel(t *testing.T) {
	registeredMessages := make(map[string]struct{}, len(AllManifestRefusals))
	for _, refusal := range AllManifestRefusals {
		registeredMessages[refusal.Error()] = struct{}{}
	}

	declaredMessages := manifestSentinelMessagesFromSource(t, "manifest_binding.go")
	require.NotEmpty(t, declaredMessages, "parsed no ErrManifest sentinels — the parser or the file layout changed")

	// Matching by message only proves registration while the messages are distinct: two sentinels
	// sharing text would let an unregistered one borrow the other's entry.
	sentinelsByMessage := make(map[string][]string, len(declaredMessages))
	for name, message := range declaredMessages {
		sentinelsByMessage[message] = append(sentinelsByMessage[message], name)
	}
	for message, sentinels := range sentinelsByMessage {
		sort.Strings(sentinels)
		require.Len(t, sentinels, 1,
			"%v share the message %q, so matching the registry by message no longer proves each is registered",
			sentinels, message)
	}

	for name, message := range declaredMessages {
		require.Contains(t, registeredMessages, message,
			"%s is not in AllManifestRefusals; append it there and classify it in so/handler", name)
	}
}

func TestAllManifestRefusalsIncludesTheBorrowedLeafIDSentinel(t *testing.T) {
	require.Contains(t, AllManifestRefusals, ErrDuplicateLeafID,
		"BindManifest returns ErrDuplicateLeafID from the sender-package parser, so refusal buckets must cover it")
}

// manifestSentinelMessagesFromSource returns declared ErrManifest* sentinel names mapped to their
// message literals.
func manifestSentinelMessagesFromSource(t *testing.T, filename string) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	require.NoError(t, err)

	messages := make(map[string]string)
	for _, decl := range file.Decls {
		genDecl, isGenDecl := decl.(*ast.GenDecl)
		if !isGenDecl || genDecl.Tok != token.VAR {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, isValueSpec := spec.(*ast.ValueSpec)
			if !isValueSpec {
				continue
			}
			for i, name := range valueSpec.Names {
				if !strings.HasPrefix(name.Name, "ErrManifest") || i >= len(valueSpec.Values) {
					continue
				}
				if message, ok := errorfMessageLiteral(valueSpec.Values[i]); ok {
					messages[name.Name] = message
				}
			}
		}
	}
	return messages
}

// errorfMessageLiteral pulls the constant message out of a fmt.Errorf("...") with no format verbs.
func errorfMessageLiteral(expr ast.Expr) (string, bool) {
	call, isCall := expr.(*ast.CallExpr)
	if !isCall || len(call.Args) != 1 {
		return "", false
	}
	literal, isLiteral := call.Args[0].(*ast.BasicLit)
	if !isLiteral || literal.Kind != token.STRING {
		return "", false
	}
	message, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return message, true
}
