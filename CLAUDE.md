# Spark Repository

Spark is a Bitcoin Layer 2 protocol enabling instant, low-cost Bitcoin transactions using FROST threshold signatures.
For deep protocol and architecture context, see [`.claude/CLAUDE.md`](.claude/CLAUDE.md).

## Setup

```bash
mise trust && mise install
lefthook install  # git hooks (optional)
```

## Components

- **`spark/`** - Go backend (Signing Operators) - see [`spark/CLAUDE.md`](spark/CLAUDE.md)
- **`sdks/js/`** - JavaScript/TypeScript SDK - see [`sdks/js/packages/spark-sdk/CLAUDE.md`](sdks/js/packages/spark-sdk/CLAUDE.md)
- **`signer/`** - Rust FROST signing implementation
- **`lrc20.dev/`** - BTKN token layer (Rust)
- **`protos/`** - Protocol buffer definitions

## Code Quality Guidelines

### Comments

Be judicious with comments. Prefer clear naming and well-structured functions over explanatory comments.

**Avoid redundant comments:**

```go
// Bad - comment just repeats the code
// Verify operator signatures
verifyOperatorSignatures(sigs)

// Good - no comment needed, function name is clear
verifyOperatorSignatures(sigs)
```

**When comments ARE useful:**

- Non-obvious "why" explanations (e.g., race conditions, protocol requirements)
- Public API documentation
- Security-critical sections
- References to external specs or papers

**Comment quality guidelines:**

- Explain "why", not "what" - the code shows what happens; comments explain reasoning
- Comments must be self-contained - never reference "the old implementation" since future readers won't have that context
- Only add non-obvious information - `// Save to database` before `db.Save()` wastes space
- Don't comment out code - delete it; git remembers
- Link to issues/specs for complex business logic

### Logging (Go)

We use **Zap** (`go.uber.org/zap`) for all logging, replacing `log/slog` from the standard library. Zap has two logger types:

- **`Logger`** (default from `logging.GetLoggerFromContext(ctx)`) — use for regular messages or messages with well-known attributes.
- **`SugaredLogger`** — use for printf-style formatting via `logger.Sugar()`.

**When to use which:**

- Default to `Logger` for most logging. Use structured fields (`zap.String`, `zap.Error`, etc.) only for well-established, indexable keys (e.g., `identity_public_key`, `transfer_id`, `token_create_id`).
- Use `Sugar()` when you need printf-style formatting with multiple dynamic values that don't need to be indexed.
- If unsure whether something should be an attribute, it probably shouldn't be — use `Sugar()` instead.

```go
// Good - structured field for a well-known, indexable key
logger.Info("transfer completed", zap.String("transfer_id", transferID))

// Good - Sugar for printf-style with dynamic values
logger.Sugar().Infof("transfer completed for %s", transferID)

// Good - Sugar for multiple dynamic values
logger.Sugar().Infof("rate limiter: enabled %t, window %s, max %d", enabled, window, max)

// Good - combining structured attributes with Sugar
logger.With(zap.Error(err)).Sugar().Infof("failed to broadcast node tx for node %s", node.ID)

// Bad - fmt.Sprintf in Logger message (defeats structured logging, use Sugar instead)
logger.Info(fmt.Sprintf("transfer completed for %s", transferID))

// Bad - generic attribute names that cause type conflicts in OpenSearch
logger.Info("validation failed", zap.String("got", value), zap.Int("count", n))
```

**Structured fields:** Only use `zap.String`/`zap.Int`/etc. for reusable, well-established keys. Check existing usage in the codebase before introducing a new key. Avoid generic attribute names (`got`, `expected`, `count`, `duration`) — these cause type conflicts in OpenSearch when different log sites use different types for the same key.

**Converting between loggers:** Use `logger.Sugar()` to get a `SugaredLogger`, and `sugaredLogger.Desugar()` to get back a `Logger`. Use `logger.With(fields...)` to create a logger that includes attributes in all subsequent logs (used in middlewares to attach `identity_public_key`, etc.).

### Function Length

Keep functions focused and reasonably short. If a function is doing too many things, consider splitting it - but avoid excessive indirection that makes code harder to follow.

### Testing

- Test public interfaces, not internal implementation details
- Focus on corner cases and error conditions, not just happy paths
- Write tests that would catch real bugs

## Pull Request Descriptions

PRs are synced to a public mirror. Follow the format in `.github/PULL_REQUEST_TEMPLATE.md`. The `## Public` section is required and CI will fail without it.

## Critical Thinking

Always critically evaluate suggestions, even when they seem reasonable.

**Be direct:**

- Question assumptions - don't just agree, analyze if there are better approaches
- Challenge decisions that don't fit logically
- Point out inconsistencies and potential issues
- Suggest alternative solutions

**Be thorough:**

- Read documentation and issues completely before responding
- Admit "I don't know" instead of guessing

**Examples:**

- ✅ "I disagree - that belongs in a different file because..."
- ✅ "Have you considered this alternative approach?"
- ✅ "This seems inconsistent with the pattern we established..."
- ❌ Implementing suggestions without evaluation
