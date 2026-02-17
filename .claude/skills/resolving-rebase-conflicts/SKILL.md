# Resolving Atlas Migration Conflicts During Rebase

## Triggers

Activate this skill when any of these conditions are detected:

- User is in a `git rebase` or `gt restack` and `atlas.sum` has conflict markers
- User mentions atlas migration conflicts, migration ordering issues, or `atlas.sum` conflicts
- `git status` shows `atlas.sum` as "both modified" or migration `.sql` files with conflicts
- User asks about resolving migration timestamp collisions after rebase

## Problem

When both upstream (main) and a feature branch add atlas migration files, `git rebase` / `gt restack` causes `atlas.sum` to conflict. The `.sql` files themselves don't conflict (they're new files), but their timestamps may overlap or sort before upstream migrations. Atlas requires migrations to be ordered chronologically and `atlas.sum` to be consistent.

## Solution

The repository includes `scripts/fix-atlas-conflicts.sh` (also available as `mise fix-atlas-conflicts`) which automates the fix:

1. Finds migration `.sql` files added by the rebasing commit (via `REBASE_HEAD`)
2. Determines the highest timestamp among all other (upstream) migrations
3. Renames our files to timestamps after the upstream max (offset +1000, increment by 1)
4. Regenerates `atlas.sum` from scratch using `atlas migrate hash`
5. Stages all changes

## Steps

1. **Preview the fix** (always do this first):
   ```bash
   mise fix-atlas-conflicts -- --dry-run
   ```
   This shows which files will be renamed and to what timestamps, without making changes.

2. **Apply the fix**:
   ```bash
   mise fix-atlas-conflicts
   ```

3. **Continue the rebase**. The script will suggest the correct command:
   - If using Graphite (`gt` on PATH and `.graphite/` exists): `gt continue`
   - Otherwise: `git rebase --continue`

## Edge Cases

- **No migrations in commit**: Script exits cleanly with "Nothing to do"
- **Outside of rebase**: Use `mise fix-atlas-conflicts -- --commit <SHA>` to specify the commit manually
- **Multiple migrations in one commit**: All are renamed with sequential timestamps, preserving relative order
- **Collision detection**: Script checks that renamed files don't collide with existing files before proceeding

## What NOT to Do

- Do not manually edit `atlas.sum` conflict markers - the hash file is regenerated from scratch
- Do not manually rename migration files without regenerating `atlas.sum`
- Do not use `git checkout --theirs atlas.sum` - this loses the entry for our migration files
