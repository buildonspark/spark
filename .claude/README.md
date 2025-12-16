# SparkClaude
Spark Claude Configuration

## Slash Command Workflow

This repository includes custom slash commands for working with implementation plans. Here's how to use them:

### Workflow tl;dr
First, edit your `.claude/spark_config.json` to set the path to your SSP (webdev) repository. The SO paths are automatically determined since this config is now inside the Spark repository. Additionally, this is not necessary, but you can edit your `.claude/settings.local.json` to allow Claude access to run commands, at a minimum I recommend at least adding all of the Bash & Read commands on this list, but modified to match your local file routes: https://github.com/kphurley7/SparkClaude/blob/main/.claude/settings.local.json.

Create new worktrees from which to have claude code operate and update them to latest
```bash
/setup_worktrees <branch_name> latest
````

Have Claude come up with a plan for what you'd like to accomplish
```bash
/create_plan <Describe what you'd like to accomplish>
```
This will write a thoughts file.  You should review this and see if the plan is what you are expecting and if it makes sense for what you want to do.  If it doesn't or you want to make changes to it, you will use:

```bash
/iterate_plan thoughts/shared/plans/2025-01-15-add-auth.md <Describe what changes you'd like>
```

Once you're happy with the plan, you can implement it:
```bash
/implement_plan thoughts/shared/plans/2025-01-15-add-auth.md
```

Now, verify the implementation matches its plan and verifies success criteria:

```bash
/validate_plan thoughts/shared/plans/2025-01-15-add-auth.md
```

Now, make sure the code is clean and the tests are working:
```bash
/clean_code both    # Check both codebases
```

Optionally, you can do a security review of the flow you changed:
```bash
/spark_sec_review <Describe what you'd like to review>
```


### Planning Commands

#### `/create_plan` - Create a New Implementation Plan

Creates a detailed implementation plan through an interactive, iterative process.

**Usage:**
```bash
/create_plan
/create_plan thoughts/allison/tickets/eng_1234.md
/create_plan think deeply about thoughts/allison/tickets/eng_1234.md
```

**What it does:**
- Interactively gathers requirements and context
- Spawns research agents to understand the codebase
- Creates structured implementation phases
- Writes measurable success criteria (automated + manual)
- Outputs to `thoughts/shared/plans/YYYY-MM-DD-description.md`

**Variants:**
- `/create_plan` - Project-specific version with humanlayer references
- `/create_plan_generic` - Generic version for any project
- `/create_plan_nt` - No thoughts directory (doesn't use thoughts sync)

#### `/iterate_plan` - Modify an Existing Plan

Updates an existing implementation plan based on feedback.

**Usage:**
```bash
/iterate_plan thoughts/shared/plans/2025-01-15-add-auth.md
/iterate_plan thoughts/shared/plans/2025-01-15-add-auth.md - add phase for password reset
```

**What it does:**
- Reads the existing plan completely
- Researches code if changes require new technical understanding
- Makes surgical edits to the plan document
- Updates phases, success criteria, or scope
- Does NOT implement code - only updates the plan

**Key characteristics:**
- Planning-focused (editing the plan, not writing code)
- Research-driven (may spawn sub-tasks for new requirements)
- Surgical edits using Edit tool
- Interactive (confirms understanding before changes)

### Implementation Commands

#### `/implement_plan` - Execute an Approved Plan

Implements an approved technical plan step-by-step.

**Usage:**
```bash
/implement_plan thoughts/shared/plans/2025-01-15-add-auth.md
```

**What it does:**
- Reads the plan and any referenced files
- Implements each phase sequentially
- Runs automated verification (tests, linting, etc.)
- Checks off completed items in the plan
- Pauses for human manual verification after each phase

**Key characteristics:**
- Execution-focused (writing code, not changing the plan)
- Phase-by-phase (completes one phase before moving to next)
- Verification-heavy (runs success criteria checks continuously)
- Updates checkboxes in the plan as work completes

### Validation Commands

#### `/validate_plan` - Verify Implementation

Validates that an implementation matches its plan and verifies success criteria.

**Usage:**
```bash
/validate_plan thoughts/shared/plans/2025-01-15-add-auth.md
```

### Code Quality Commands

#### `/clean_code` - Run Code Quality Checks

Runs comprehensive code quality checks (linting, formatting, type-checking, tests) for SO and/or SSP.

**Usage:**
```bash
/clean_code so      # Check SO codebase only
/clean_code ssp     # Check SSP codebase only
/clean_code both    # Check both codebases
```

**What it does:**
- **SO**: Runs `mise lint` and `mise test-go`
- **SSP**: Runs `ruff format`, `ruff check`, `pyre`, and `pytest`
- Reports success/failure for each check
- Provides clear summary of results

**When to use:**
- After making any code changes
- Before creating commits or PRs
- After resolving merge conflicts

### Other Workflow Commands

#### `/setup_worktrees` - Create Development Worktrees

Creates git worktrees for both SO and SSP codebases with branch suffixes.

**Usage:**
```bash
/setup_worktrees feature-auth
/setup_worktrees feature-auth latest
```

**What it does:**
- **Safety check**: Verifies both repos have no uncommitted changes (errors if dirty)
- Creates SO worktree with branch name `{branch}_so`
- Creates SSP worktree with branch name `{branch}_ssp`
- Optionally pulls latest from main if "latest" flag provided
- Places worktrees as siblings to main repositories

**Important:** Both repositories must be clean (no uncommitted changes) before running.

**Example:**
```bash
/setup_worktrees feature-auth latest
```
Creates worktrees with branch suffixes `_so` and `_ssp` as siblings to main repos (paths read from `spark_config.json`)

#### `/research_codebase` - Document Codebase

Documents the codebase as-is with historical context stored in thoughts directory.

#### `/commit` - Create Git Commits

Creates git commits with user approval (no Claude attribution).

#### `/describe_pr` - Generate PR Descriptions

Generates comprehensive PR descriptions following repository templates.

## Typical Workflow

The standard development workflow using these commands:

```
1. Create plan
   /create_plan → generates initial implementation plan

2. Iterate plan (optional)
   /iterate_plan → refine based on feedback

3. Implement plan
   /implement_plan → execute the approved plan

4. Validate plan (optional)
   /validate_plan → verify implementation matches plan

5. Commit & PR
   /commit → create commits
   /describe_pr → generate PR description
```

### Quick Reference

| Command | Purpose | Modifies Code? | Modifies Plan? |
|---------|---------|----------------|----------------|
| `/create_plan` | Generate new implementation plan | No | Creates new |
| `/iterate_plan` | Update existing plan | No | Yes |
| `/implement_plan` | Execute plan and write code | Yes | Updates checkboxes |
| `/validate_plan` | Verify implementation | No | No |

### Command Variants

**create_plan variants:**
- `create_plan.md` - Project-specific with humanlayer references
- `create_plan_generic.md` - Generic version for any project
- `create_plan_nt.md` - No thoughts directory integration

**iterate_plan variants:**
- `iterate_plan.md` - Project-specific with humanlayer references
- `iterate_plan_nt.md` - No thoughts directory integration
