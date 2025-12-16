---
description: Run code quality checks (linting, formatting, type-checking, tests) for SO and/or SSP
---

# Clean Code

Runs comprehensive code quality checks including linting, formatting, type-checking, and tests for the specified repository.

## Usage

```bash
/clean_code so      # Run checks on SO (Signing Operator) codebase
/clean_code ssp     # Run checks on SSP (Spark Service Provider) codebase
/clean_code both    # Run checks on both SO and SSP codebases
```

## What This Command Does

Automatically runs all required code quality commands for the specified codebase(s):

### For SO (Signing Operator):
1. **Linting**: `mise lint`
2. **Unit Tests**: `mise test-go`
3. **SDK Workspace Build**: `cd sdks/js && yarn build` (validates all SDK packages and examples)
4. **SDK Examples Formatting**: `cd sdks/js/examples/nodejs-scripts && yarn prettier --check .`

### For SSP (Spark Service Provider):
1. **Formatting**: `uv run ruff format .`
2. **Linting**: `uv run ruff check . --fix`
3. **Type Checking**: `uv run pyre`
4. **Unit Tests**: `env -u QUART_CONFIG pytest -m "not minikube"`

## Implementation Instructions

When this command is invoked:

1. **Determine repository paths:**
   - **SO path**: Automatically determined as the Spark repository root (where `.claude/spark_config.json` is located)
   - **SSP path**: Read `spark_config.json` and extract `codebase_locations.ssp.path`

2. **Parse the parameter:**
   - Extract which repo(s) to check: `so`, `ssp`, or `both`
   - Validate the parameter

3. **If no parameter provided:**
   ```
   Please specify which codebase to check.

   Usage: /clean_code <so|ssp|both>

   Examples:
   - /clean_code so     # Check SO codebase only
   - /clean_code ssp    # Check SSP codebase only
   - /clean_code both   # Check both codebases
   ```
   Stop and wait for user input.

4. **Run SO checks** (if `so` or `both`):
   ```bash
   cd {SO_PATH}/spark

   echo "Running SO linting..."
   mise lint

   echo "Running SO unit tests..."
   mise test-go

   echo "Running SDK workspace build..."
   cd sdks/js && yarn build

   echo "Running SDK examples formatting check..."
   cd examples/nodejs-scripts && yarn prettier --check .
   ```
   - Report success/failure for each step
   - Stop if any command fails (unless user wants to continue)

5. **Run SSP checks** (if `ssp` or `both`):
   ```bash
   cd {SSP_PATH}/sparkcore

   echo "Running SSP formatting..."
   uv run ruff format .

   echo "Running SSP linting..."
   uv run ruff check . --fix

   echo "Running SSP type checking..."
   uv run pyre

   echo "Running SSP unit tests..."
   env -u QUART_CONFIG pytest -m "not minikube"
   ```
   - Report success/failure for each step
   - Stop if any command fails (unless user wants to continue)

6. **Provide summary:**
   ```
   ✓ Code quality checks completed!

   SO Results:
   ✓ Linting: Passed
   ✓ Unit Tests: Passed
   ✓ SDK Workspace Build: Passed
   ✓ SDK Examples Formatting: Passed

   SSP Results:
   ✓ Formatting: Passed
   ✓ Linting: Passed
   ✓ Type Checking: Passed
   ✓ Unit Tests: Passed

   All checks passed! Your code is ready for commit.
   ```

   Or if there were failures:
   ```
   ✗ Code quality checks failed

   SO Results:
   ✓ Linting: Passed
   ✗ Unit Tests: Failed (see output above)
   ✓ SDK Workspace Build: Passed
   ✗ SDK Examples Formatting: Failed (see output above)

   SSP Results:
   ✓ Formatting: Passed
   ✓ Linting: Passed
   ✗ Type Checking: Failed (see output above)
   ✗ Unit Tests: Failed (see output above)

   Please fix the issues above before committing.
   ```

## Error Handling

- **Invalid parameter:** Show usage and valid options
- **Missing config file:** Inform user that `spark_config.json` is required
- **Invalid repo path:** Check if directories exist before running commands
- **Command failure:**
  - Show the full error output
  - Ask if user wants to continue with remaining checks or stop
  - Mark which checks failed in the final summary

## Important Notes

- Commands are run in the appropriate directories for each repo
- All output from commands is shown to the user
- Checks are run sequentially, not in parallel
- If a check fails, the command continues to run remaining checks (unless user aborts)
- The final summary clearly shows which checks passed/failed

## Examples

### Check SO only:
```bash
/clean_code so
```
Runs:
1. SO linting
2. SO unit tests
3. SDK workspace build (all packages and examples)
4. SDK examples formatting check

### Check SSP only:
```bash
/clean_code ssp
```
Runs:
1. SSP formatting
2. SSP linting
3. SSP type checking
4. SSP unit tests

### Check both:
```bash
/clean_code both
```
Runs all checks for both SO and SSP in sequence.

## When to Use This Command

**You MUST use this command:**
- After making any code changes to SO or SSP
- Before creating a commit
- Before creating a pull request
- After resolving merge conflicts

**Best practice:**
- Run `/clean_code both` at the end of every development session
- Run checks frequently during development to catch issues early
- Fix issues immediately rather than letting them accumulate
