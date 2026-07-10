// Package fmtlint provides a golangci-lint plugin that flags redundant formatting of printf-style arguments.
//
// It reports two patterns:
//
// 1. A value that implements fmt.Stringer (i.e. has a `String() string` method) passed to %s, %v, or %q: fmt invokes
// String() automatically, so calling it explicitly is redundant. As a special case, keys.ToHex is also covered.
//
//	fmt.Errorf("transfer %s has invalid type", id.String()) // .String() is redundant
//	fmt.Errorf("transfer %s has invalid type", id)          // equivalent
//
//	fmt.Errorf("key %s has invalid type", pubKey.ToHex())   	// .ToHex() is redundant
//	fmt.Errorf("key %s has invalid type", pubKey)   			// equivalent
//
// 2. hex.EncodeToString(b) passed to %s or %v: the %x verb hex-encodes the raw bytes directly.
//
//	fmt.Errorf("refund tx mismatch, got: %s", hex.EncodeToString(b)) // unnecessary hex.EncodeToString
//	fmt.Errorf("refund tx mismatch, got: %x", b)                     // equivalent
//
// Both patterns also apply inside testify assert/require calls, whose trailing message is formatted the same way.
//
// 3. A print-style (non-formatting) function called with a format string and further arguments: these functions
// concatenate their arguments via fmt.Sprint, so the verbs print literally and the author almost certainly meant the
// printf variant.
//
//	logger.Sugar().Info("querying transfer with ID: %s", id)  // logs "querying transfer with ID: %s<id>"
//	logger.Sugar().Infof("querying transfer with ID: %s", id) // intended
package fmtlint

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

func init() {
	register.Plugin("fmtlint", New)
}

// Settings holds plugin configuration. It's required by golangci-lint's API.
type Settings struct{}

type Plugin struct{}

func New(settings any) (register.LinterPlugin, error) {
	if _, err := register.DecodeSettings[Settings](settings); err != nil {
		return nil, err
	}
	return &Plugin{}, nil
}

func (p *Plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{
		{
			Name: "fmtlint",
			Doc:  "reports incorrect formatting of printf-style arguments",
			Run:  run,
		},
	}, nil
}

func (p *Plugin) GetLoadMode() string {
	// We need type information to confirm that the receiver of .String() implements fmt.Stringer.
	return register.LoadModeTypesInfo
}

// formatFuncs is the set of printf-style functions whose final-but-one string parameter is a format string consumed by
// fmt-style verbs. Keys are the values returned by (*types.Func).FullName().
//
// There are too many testify functions to list here, so they're handled in formatCallArgIndex.
//
// Excludes scanning functions (Sscanf, Fscanf, …) whose arguments are scan targets rather than values to format.
//
// The entries also anchor the print-style check: a call to a function whose name plus "f" appears here (e.g.
// SugaredLogger.Info) is checked for format verbs that belong in the f-variant — see printfVariant.
var formatFuncs = map[string]bool{
	"fmt.Errorf":  true,
	"fmt.Sprintf": true,
	"fmt.Printf":  true,
	"fmt.Fprintf": true,
	"fmt.Appendf": true,

	"(*testing.common).Logf":   true,
	"(*testing.common).Skipf":  true,
	"(*testing.common).Errorf": true,
	"(*testing.common).Fatalf": true,
	"(testing.TB).Logf":        true,
	"(testing.TB).Skipf":       true,
	"(testing.TB).Errorf":      true,
	"(testing.TB).Fatalf":      true,

	"(*go.uber.org/zap.SugaredLogger).Logf":    true,
	"(*go.uber.org/zap.SugaredLogger).Debugf":  true,
	"(*go.uber.org/zap.SugaredLogger).Infof":   true,
	"(*go.uber.org/zap.SugaredLogger).Warnf":   true,
	"(*go.uber.org/zap.SugaredLogger).Errorf":  true,
	"(*go.uber.org/zap.SugaredLogger).DPanicf": true,
	"(*go.uber.org/zap.SugaredLogger).Panicf":  true,
	"(*go.uber.org/zap.SugaredLogger).Fatalf":  true,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			checkCall(pass, call)
			return true
		})
	}
	return nil, nil
}

func checkCall(pass *analysis.Pass, call *ast.CallExpr) {
	// A spread call (f(format, args...)) hides the mapping between verbs and arguments, so we can't reason about it.
	if call.Ellipsis != token.NoPos {
		return
	}

	fn := typeutil.StaticCallee(pass.TypesInfo, call)
	if fn == nil {
		// typeutil.StaticCallee returns nil for calls through an interface (e.g. testing.TB.Errorf), because the concrete
		// implementation isn't known statically. That's irrelevant here: the format string and its arguments are fixed at
		// the call site regardless of the dynamic type, so we resolve the interface method ourselves.
		fn = interfaceCallee(pass.TypesInfo, call)
		if fn == nil {
			return
		}
	}

	if fVariant, ok := printfVariant(fn); ok {
		checkPrintCall(pass, call, fn, fVariant)
		return
	}

	fmtIdx, ok := formatCallArgIndex(fn)
	if !ok || fmtIdx >= len(call.Args) {
		return
	}

	format, ok := constStringValue(pass, call.Args[fmtIdx])
	if !ok {
		return
	}

	verbs, reliable := parseVerbs(format)
	if !reliable {
		return
	}

	for _, v := range verbs {
		argPos := fmtIdx + 1 + v.argIndex
		if argPos >= len(call.Args) {
			continue
		}
		arg := call.Args[argPos]

		if v.invokesStringer() {
			if sel, call2, ok := redundantStringCall(pass, arg); ok {
				reportRedundantString(pass, sel, call2, v.verb)
			}
		}
		// A plain %s or %v prints the hex string as-is, which a plain %x produces directly from the raw bytes. Anything
		// that makes %x diverge from %s/%v is excluded: %q quotes; %#x adds a "0x" prefix (and %#v uses GoStringer); the
		// space flag inserts byte separators (% x); and precision counts input bytes for %x but output runes for %s/%v.
		if (v.verb == 's' || v.verb == 'v') && !v.sharpFlag && !v.spaceFlag && !v.hasPrecision {
			if inner, call2, ok := hexEncodeCall(pass, arg); ok {
				reportHexEncode(pass, call.Args[fmtIdx], v, call2, inner)
			}
		}
	}
}

// interfaceCallee returns the method selected by a call through an interface value, or nil if the call isn't an
// interface-method call. It complements typeutil.StaticCallee, which excludes interface methods.
func interfaceCallee(info *types.Info, call *ast.CallExpr) *types.Func {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	selection := info.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal {
		return nil
	}
	fn, ok := selection.Obj().(*types.Func)
	if !ok {
		return nil
	}
	recv := fn.Type().(*types.Signature).Recv()
	if recv == nil || !types.IsInterface(recv.Type()) {
		return nil
	}
	return fn
}

// formatCallArgIndex reports whether fn is a call whose arguments this linter reasons about, and if so the index (within
// call.Args, which excludes the receiver) of its format string.
func formatCallArgIndex(fn *types.Func) (fmtIdx int, ok bool) {
	switch {
	case formatFuncs[fn.FullName()]:
		return formatArgIndex(fn)
	case isTestifyPkg(fn.Pkg()):
		return checkTestifyFn(fn)
	default:
		return 0, false
	}
}

func checkTestifyFn(fn *types.Func) (int, bool) {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || !sig.Variadic() {
		return 0, false
	}
	// In the testify packages, an assertion name ending in "f" is always the printf variant (e.g. Equalf), whose format
	// string is the `msg string` parameter before the variadic, just like the fmt functions. This suffix check also
	// separates Failf from Fail, whose leading `failureMessage string` parameter isn't a format string.
	if strings.HasSuffix(fn.Name(), "f") {
		return formatArgIndex(fn)
	}
	// Non-f assertion: the format string, when present, is the first variadic element. testify only treats it as a
	// format string when it's followed by more arguments; otherwise, parseVerbs' arguments dont resolve to any call arg
	// and nothing is reported.
	return sig.Params().Len() - 1, true
}

func isTestifyPkg(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}
	path := pkg.Path()
	return path == "github.com/stretchr/testify/assert" || path == "github.com/stretchr/testify/require"
}

// formatArgIndex returns the index (within call.Args, which excludes the receiver) of the format string parameter.
// For fmt/zap functions and testify's "f" variants, the format string is the parameter immediately preceding the
// variadic one.
func formatArgIndex(fn *types.Func) (int, bool) {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || !sig.Variadic() {
		return 0, false
	}
	params := sig.Params()
	if params.Len() < 2 {
		return 0, false
	}
	idx := params.Len() - 2
	if !isBasicString(params.At(idx).Type()) {
		return 0, false
	}
	return idx, true
}

// printfVariant reports whether fn is the print-style counterpart of a registered printf-style function — that is,
// appending "f" to its name yields a formatFuncs entry (`Info` -> `Infof`) and returns the counterpart's name. The fmt
// and testing packages are excluded: govet's printf check already reports formatting directives passed to their print
// functions, so flagging them here would double-report.
func printfVariant(fn *types.Func) (string, bool) {
	if pkg := fn.Pkg(); pkg == nil || pkg.Path() == "fmt" || pkg.Path() == "testing" {
		return "", false
	}
	if !formatFuncs[fn.FullName()+"f"] {
		return "", false
	}
	return fn.Name() + "f", true
}

// checkPrintCall flags a print-style call whose first variadic argument is a constant string containing format verbs
// followed by more arguments. Print-style functions concatenate via fmt.Sprint, so the verbs print literally — the
// author almost certainly intended the printf variant.
func checkPrintCall(pass *analysis.Pass, call *ast.CallExpr, fn *types.Func, fVariant string) {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || !sig.Variadic() {
		return
	}
	strIdx := sig.Params().Len() - 1
	// Require arguments after the candidate format string: verbs with nothing to consume print literally, which may
	// well be intentional (e.g. logging a template).
	if len(call.Args) <= strIdx+1 {
		return
	}
	format, ok := constStringValue(pass, call.Args[strIdx])
	if !ok {
		return
	}
	verbs, reliable := parseVerbs(format)
	if !reliable {
		return
	}
	for _, v := range verbs {
		// A '%' in prose followed by a space and a word starting with a verb letter ("100% done") parses as a valid
		// space-flagged verb. The space flag is vanishingly rare in real format strings, so don't count it as evidence
		// that formatting was intended.
		if v.spaceFlag {
			continue
		}
		reportPrintWithVerb(pass, call, fn.Name(), fVariant, v.verb)
		return
	}
}

func reportPrintWithVerb(pass *analysis.Pass, call *ast.CallExpr, name, fVariant string, verb byte) {
	diag := analysis.Diagnostic{
		Pos:      call.Fun.Pos(),
		End:      call.Fun.End(),
		Category: "fmtlint",
		Message:  fmt.Sprintf("%s call has format directive %%%c: use %s", name, verb, fVariant),
	}
	if ident := calleeNameIdent(call); ident != nil {
		diag.SuggestedFixes = []analysis.SuggestedFix{{
			Message:   fmt.Sprintf("rename to %s", fVariant),
			TextEdits: []analysis.TextEdit{{Pos: ident.Pos(), End: ident.End(), NewText: []byte(fVariant)}},
		}}
	}
	pass.Report(diag)
}

// calleeNameIdent returns the identifier naming the called function, so a suggested fix can rewrite it.
func calleeNameIdent(call *ast.CallExpr) *ast.Ident {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel
	case *ast.Ident:
		return fun
	}
	return nil
}

func constStringValue(pass *analysis.Pass, expr ast.Expr) (string, bool) {
	tv, ok := pass.TypesInfo.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

type verbArg struct {
	verb         byte
	argIndex     int  // 0-based index into the variadic arguments
	offset       int  // byte offset of the verb letter within the (decoded) format string
	sharpFlag    bool // whether the '#' flag was present (e.g. %#v)
	spaceFlag    bool // whether the ' ' flag was present (e.g. "% x"); separates hex bytes but is a no-op for %s/%v
	hasPrecision bool // whether a precision was present (e.g. "%.3s"); counts input bytes for %x but runes for %s/%v
}

// invokesStringer reports whether fmt would invoke fmt.Stringer for this verb.
// %#v is excluded: it uses fmt.GoStringer and prints the Go-syntax representation, so a bare Stringer value formats
// differently than its String() result.
func (v verbArg) invokesStringer() bool {
	switch v.verb {
	case 's', 'q':
		return true
	case 'v':
		return !v.sharpFlag
	}
	return false
}

// parseVerbs maps each format verb to the variadic argument it consumes.
//
// reliable is false if the format string uses argument-indexed width or precision (%*d), explicit argument indexes
// (%[2]s), or a byte that isn't a fmt verb where a verb is expected. The former break the simple positional mapping;
// the latter means the string is either a broken format or prose containing a '%' (e.g. "improved by 5%!"). In all
// cases the parse isn't meaningful, so callers shouldn't act on the result.
func parseVerbs(format string) (verbs []verbArg, reliable bool) {
	argNum := 0
	end := len(format)
	for i := 0; i < end; i++ {
		offset := strings.IndexByte(format[i:], '%')
		if offset == -1 {
			break
		}
		i += offset + 1
		if i >= end {
			// Trailing '%' with no verb: the format is malformed, so don't trust the parse.
			return nil, false
		}
		if format[i] == '%' {
			continue
		}

		// Consume flags, width and precision. A '*' (arg-based width/precision) or '[' (explicit argument index) breaks
		// positional mapping.
		sharp := false
		space := false
		precision := false
		for i < end {
			switch format[i] {
			case '*', '[':
				return nil, false
			case '#':
				sharp = true
				i++
				continue
			case ' ':
				space = true
				i++
				continue
			case '.':
				precision = true
				i++
				continue
			case '+', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
				i++
				continue
			}
			break
		}
		if i >= end {
			// Flags/width consumed the rest of the string with no verb: malformed, so don't trust the parse.
			return nil, false
		}
		if !isFmtVerb(format[i]) {
			return nil, false
		}

		verbs = append(verbs, verbArg{verb: format[i], argIndex: argNum, offset: i, sharpFlag: sharp, spaceFlag: space, hasPrecision: precision})
		argNum++
	}
	return verbs, true
}

// isFmtVerb reports whether b is a formatting verb documented by the fmt package (plus %w from fmt.Errorf).
func isFmtVerb(b byte) bool {
	return strings.IndexByte("vTtbcdoOqxXUeEfFgGspw", b) >= 0
}

// redundantStringCall reports whether arg is an `x.String()` call where x's type (as it would be boxed into the `any`
// passed to fmt) implements fmt.Stringer, or a `x.ToHex()` call where x is a key.
func redundantStringCall(pass *analysis.Pass, arg ast.Expr) (*ast.SelectorExpr, *ast.CallExpr, bool) {
	call, ok := arg.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || call.Ellipsis != token.NoPos {
		return nil, nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}

	switch sel.Sel.Name {
	case "String":
		if !fmtInvokesStringer(pass.TypesInfo.TypeOf(sel.X)) {
			return nil, nil, false
		}
		return sel, call, true
	case "ToHex":
		// fmt doesn't auto-invoke ToHex, so this is only redundant because the keys types implement fmt.Stringer with
		// String()==ToHex(): %s/%v/%q on the bare receiver yields the same hex. Require both before flagging.
		t := pass.TypesInfo.TypeOf(sel.X)
		if !isKey(t) || !fmtInvokesStringer(t) {
			return nil, nil, false
		}
		return sel, call, true
	}
	return nil, nil, false
}

// fmtInvokesStringer reports whether fmt would actually call t's String() method for a Stringer-invoking verb. fmt's
// precedence is Formatter, then error, then Stringer (see the fmt package docs), so a type that also implements
// fmt.Formatter or error formats via one of those instead — its output can diverge from String(), meaning .String() is
// not redundant.
func fmtInvokesStringer(t types.Type) bool {
	return implementsStringer(t) && !implementsFmtFormatter(t) && !implementsError(t)
}

// isKey reports whether typ is one of the github.com/lightsparkdev/spark/common/keys types or a pointer to one.
func isKey(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := typ.(*types.Named)
	if !ok {
		return false
	}
	pkg := named.Obj().Pkg()
	return pkg != nil && pkg.Path() == "github.com/lightsparkdev/spark/common/keys"
}

// implementsStringer reports whether t's method set contains `String() string`.
//
// Using the method set (rather than just checking that x.String() resolves) is important: if String has a pointer
// receiver and x is an addressable value of type T, then x.String() compiles, but the value T boxed into `any` for fmt
// doesn't implement fmt.Stringer; only *T does, so the call should be excluded.
func implementsStringer(t types.Type) bool {
	if t == nil {
		return false
	}
	sel := types.NewMethodSet(t).Lookup(nil, "String")
	if sel == nil {
		return false
	}
	sig, ok := sel.Type().(*types.Signature)
	if !ok {
		return false
	}
	// fmt.Stringer is `String() string`. A method returning a named string type doesn't satisfy it, so fmt wouldn't
	// auto-invoke it and .String() wouldn't be redundant.
	return sig.Params().Len() == 0 && sig.Results().Len() == 1 && isBasicString(sig.Results().At(0).Type())
}

func isBasicString(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Kind() == types.String
}

// implementsError reports whether t's method set contains `Error() string`, i.e. it satisfies the error interface.
func implementsError(t types.Type) bool {
	if t == nil {
		return false
	}
	errIface := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	return types.Implements(t, errIface)
}

// implementsFmtFormatter reports whether t's method set contains a `Format(fmt.State, rune)` method, i.e. it satisfies
// fmt.Formatter. It's checked structurally (method name + shape) rather than against the fmt.Formatter interface so we
// don't depend on the fmt package being imported by the analyzed file.
func implementsFmtFormatter(t types.Type) bool {
	if t == nil {
		return false
	}
	sel := types.NewMethodSet(t).Lookup(nil, "Format")
	if sel == nil {
		return false
	}
	sig, ok := sel.Type().(*types.Signature)
	if !ok || sig.Params().Len() != 2 || sig.Results().Len() != 0 {
		return false
	}
	// fmt.Formatter is `Format(fmt.State, rune)`: the first parameter is an interface (fmt.State), the second a rune.
	if _, isIface := sig.Params().At(0).Type().Underlying().(*types.Interface); !isIface {
		return false
	}
	b, ok := sig.Params().At(1).Type().(*types.Basic)
	return ok && b.Kind() == types.Int32
}

func reportRedundantString(pass *analysis.Pass, sel *ast.SelectorExpr, call *ast.CallExpr, verb byte) {
	strFn := sel.Sel.Name
	pass.Report(analysis.Diagnostic{
		Pos:      sel.Sel.Pos(),
		End:      call.End(),
		Category: "fmtlint",
		Message:  fmt.Sprintf("unnecessary .%s() call: %%%c already invokes the Stringer", strFn, verb),
		SuggestedFixes: []analysis.SuggestedFix{
			{
				Message: fmt.Sprintf("remove redundant .%s() call", strFn),
				TextEdits: []analysis.TextEdit{
					{Pos: sel.X.End(), End: call.End(), NewText: nil}, // Delete the trailing method call
				},
			},
		},
	})
}

// hexEncodeCall reports whether arg is a single-argument call to hex.EncodeToString, returning the inner []byte argument.
func hexEncodeCall(pass *analysis.Pass, arg ast.Expr) (inner ast.Expr, call *ast.CallExpr, ok bool) {
	call, isCall := arg.(*ast.CallExpr)
	if !isCall || len(call.Args) != 1 || call.Ellipsis != token.NoPos {
		return nil, nil, false
	}
	fn := typeutil.StaticCallee(pass.TypesInfo, call)
	if fn == nil || fn.FullName() != "encoding/hex.EncodeToString" {
		return nil, nil, false
	}
	inner = call.Args[0]
	// The fix rewrites hex.EncodeToString(x) to %x on x. For %x, fmt honors Formatter/error/Stringer before its native
	// byte-slice handling, so if x's type implements any of them, %x formats the method result (hex-encoding a
	// Stringer's string, say) rather than the raw bytes hex.EncodeToString sees. Only unwrap a plain byte slice.
	t := pass.TypesInfo.TypeOf(inner)
	if implementsStringer(t) || implementsError(t) || implementsFmtFormatter(t) {
		return nil, nil, false
	}
	return inner, call, true
}

func reportHexEncode(pass *analysis.Pass, formatArg ast.Expr, v verbArg, call *ast.CallExpr, inner ast.Expr) {
	diag := analysis.Diagnostic{
		Pos:      call.Pos(),
		End:      call.End(),
		Category: "fmtlint",
		Message:  fmt.Sprintf("unnecessary hex.EncodeToString: use the %%x verb on the raw bytes instead of %%%c", v.verb),
	}
	if edits, ok := hexEncodeFix(formatArg, v, call, inner); ok {
		diag.SuggestedFixes = []analysis.SuggestedFix{{Message: "replace with the %x verb", TextEdits: edits}}
	}
	pass.Report(diag)
}

// hexEncodeFix builds the edits that rewrite the verb to %x and unwrap the hex.EncodeToString call. It returns ok=false
// when the format isn't a single string literal, or the literal contains escape sequences that shift byte offsets
// relative to the decoded value parseVerbs operated on.
func hexEncodeFix(formatArg ast.Expr, v verbArg, call *ast.CallExpr, inner ast.Expr) ([]analysis.TextEdit, bool) {
	lit, ok := formatArg.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING || len(lit.Value) == 0 {
		return nil, false
	}
	switch lit.Value[0] {
	case '`':
		// Raw string literal: no escapes, so decoded offsets map directly.
	case '"':
		// Interpreted string: offsets only map directly without escapes. (%% isn't a Go escape, so it doesn't shift offsets.)
		if strings.Contains(lit.Value, "\\") {
			return nil, false
		}
	default:
		return nil, false
	}

	// The opening quote is at lit.Pos(); the decoded content starts one byte later.
	verbPos := lit.Pos() + token.Pos(1+v.offset)
	return []analysis.TextEdit{
		{Pos: verbPos, End: verbPos + 1, NewText: []byte{'x'}},
		// Drop "hex.EncodeToString(" and the matching ")", leaving the argument.
		{Pos: call.Pos(), End: inner.Pos(), NewText: nil},
		{Pos: inner.End(), End: call.End(), NewText: nil},
	}, true
}
