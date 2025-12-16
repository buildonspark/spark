---
description: Set up worktrees for both SO and SSP codebases with branch suffixes 
---

# Setup Worktrees

Creates git worktrees for both the SO and SSP codebases with appropriate branch name suffixes. Add 'latest' param to update both

## Usage

```bash
/setup_worktrees <branch-name>
/setup_worktrees <branch-name> latest
```

**Parameters:**
- `branch-name` (required): Base name for the branches (e.g., "feature-auth")
- `latest` (optional): If provided, pulls latest changes from main before creating branches

## What This Command Does

1. **If "latest" is specified:**
   - Switches to main branch in SO repo and pulls latest changes
   - Switches to main branch in SSP repo and pulls latest changes

2. **Creates SO worktree:**
   - Branch name: `{branch-name}_so`
   - Location: `../spark2-{branch-name}_so` (sibling to main repo)
   - Checks out from current HEAD

3. **Creates SSP worktree:**
   - Branch name: `{branch-name}_ssp`
   - Location: `../webdev-{branch-name}_ssp` (sibling to main repo)
   - Checks out from current HEAD

4. **Provides summary** of created worktrees with their paths

## Repository Locations

Repository paths are determined as follows:
- **SO Repository**: Automatically determined as the Spark repository root (where `.claude/spark_config.json` is located)
- **SSP Repository**: Read from `codebase_locations.ssp.path` in `spark_config.json`

## Implementation Instructions

When this command is invoked:

1. **Determine repository paths:**
   - **SO path**: Find the directory containing `.claude/spark_config.json` (this is the Spark repository root)
   - **SSP path**: Read `spark_config.json` and extract `codebase_locations.ssp.path`
   - Get repository names from the paths (e.g., "spark" and "webdev")

2. **Parse the input:**
   - Extract branch name (required)
   - Check if "latest" flag is present
   - Validate branch name (alphanumeric, hyphens, underscores)

3. **If no branch name provided:**
   ```
   Please provide a branch name.

   Usage: /setup_worktrees <branch-name> [latest]

   Example: /setup_worktrees feature-auth
   Example: /setup_worktrees feature-auth latest
   ```
   Stop and wait for user input.

4. **Check for uncommitted changes (CRITICAL - must be done before proceeding):**

   **Check SO repository:**
   ```bash
   cd {SO_PATH}
   git status --porcelain
   ```
   If output is not empty, there are uncommitted changes. Show error:
   ```
   ✗ Error: SO repository has uncommitted changes

   The following files have changes:
   [List the files from git status]

   Please commit or stash your changes before creating worktrees.

   To see details: cd {SO_PATH} && git status
   ```
   Stop and exit.

   **Check SSP repository:**
   ```bash
   cd {SSP_PATH}
   git status --porcelain
   ```
   If output is not empty, there are uncommitted changes. Show error:
   ```
   ✗ Error: SSP repository has uncommitted changes

   The following files have changes:
   [List the files from git status]

   Please commit or stash your changes before creating worktrees.

   To see details: cd {SSP_PATH} && git status
   ```
   Stop and exit.

   **If both repositories are clean:**
   ```
   ✓ Both repositories are clean, proceeding with worktree creation...
   ```

5. **If "latest" flag provided:**
   - In SO repo: `cd {SO_PATH} && git checkout main && git pull`
   - In SSP repo: `cd {SSP_PATH} && git checkout main && git pull`
   - Report any errors if pull fails

6. **Create SO worktree:**
   ```bash
   cd {SO_PATH}
   git worktree add ../{SO_REPO_NAME}-{branch-name}_so -b {branch-name}_so
   ```
   - Handle errors gracefully (branch already exists, etc.)
   - Report the full path of created worktree

7. **Create SSP worktree:**
   ```bash
   cd {SSP_PATH}
   git worktree add ../{SSP_REPO_NAME}-{branch-name}_ssp -b {branch-name}_ssp
   ```
   - Handle errors gracefully
   - Report the full path of created worktree

8. **Provide summary:**
   ```
   ✓ Worktrees created successfully!

   SO worktree:
     Path: {SO_PARENT_DIR}/{SO_REPO_NAME}-{branch-name}_so
     Branch: {branch-name}_so

   SSP worktree:
     Path: {SSP_PARENT_DIR}/{SSP_REPO_NAME}-{branch-name}_ssp
     Branch: {branch-name}_ssp

   Next steps:
   - cd {SO_PARENT_DIR}/{SO_REPO_NAME}-{branch-name}_so
   - cd {SSP_PARENT_DIR}/{SSP_REPO_NAME}-{branch-name}_ssp
   ```

## Error Handling

- **Uncommitted changes detected:** Stop immediately and show:
  - Which repository has uncommitted changes
  - List of modified/staged/untracked files
  - Instructions to commit or stash changes
  - Command to view full status
  - **DO NOT proceed** - this is a hard blocker

- **Branch already exists:** Inform user and ask if they want to:
  - Delete and recreate the worktree
  - Use a different branch name
  - Abort

- **Git pull fails:** Show error and ask if they want to:
  - Continue without pulling
  - Abort the operation

- **Worktree creation fails:** Show the git error message and suggest solutions

## Important Notes

- **Safety first**: The command checks for uncommitted changes before proceeding. Both repositories must be clean.
- Worktrees are created as **siblings** to the main repositories, not inside them
- Branch names will have `_so` and `_ssp` suffixes automatically added
- The "latest" flag updates the main branch, not the new worktree branches
- You can have multiple worktrees for the same repository
- If you have uncommitted changes, commit them or stash them with `git stash` before running this command

## Examples

### Basic usage:
```bash
/setup_worktrees feature-auth
```
Creates:
- `{SO_PARENT_DIR}/{SO_REPO_NAME}-feature-auth_so` (branch: feature-auth_so)
- `{SSP_PARENT_DIR}/{SSP_REPO_NAME}-feature-auth_ssp` (branch: feature-auth_ssp)

### With latest updates:
```bash
/setup_worktrees bugfix-123 latest
```
1. Pulls latest from main in both repos
2. Creates worktrees:
   - `{SO_PARENT_DIR}/{SO_REPO_NAME}-bugfix-123_so` (branch: bugfix-123_so)
   - `{SSP_PARENT_DIR}/{SSP_REPO_NAME}-bugfix-123_ssp` (branch: bugfix-123_ssp)
