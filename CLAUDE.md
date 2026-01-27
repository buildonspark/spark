# Spark Repository

Spark is a Bitcoin Layer 2 protocol enabling instant, low-cost Bitcoin transactions using FROST threshold signatures.

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

### Function Length
Keep functions focused and reasonably short. If a function is doing too many things, consider splitting it - but avoid excessive indirection that makes code harder to follow.

### Testing
- Test public interfaces, not internal implementation details
- Focus on corner cases and error conditions, not just happy paths
- Write tests that would catch real bugs

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
