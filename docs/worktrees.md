# Git worktrees in Shep

Shep discovers linked Git worktrees alongside regular projects. Each valid
worktree becomes its own candidate, carries repository and branch metadata, and
opens with a branch-specific workspace name.

## Quick path

```sh
# Show worktrees and their metadata.
shep list --format json

# Produce stable path, label, and icon columns for scripts or Television.
shep list --format tsv

# Start the interactive picker. Worktrees show a branch icon and branch name.
shep open
```

The default workspace name is `{{.RepoName}}@{{.Branch}}`. For example, the
`api` repository on `fix/auth` opens as `api@fix/auth`. A configured custom
workspace-name template still takes precedence.

## Discovery cost and boundaries

Shep gates worktree discovery with directory checks before running Git:

| Repository type | Administrative directory |
|---|---|
| Standard repository | `<repo>/.git/worktrees` |
| Bare repository | `<repo>/worktrees` |

If the applicable directory is absent or unreadable, Shep runs no worktree Git
subprocess. When it exists, Shep runs `git worktree list --porcelain` with a
50 ms timeout, accepts only absolute existing directories, and skips stale or
prunable entries. A failure keeps the primary repository candidate available.

## CLI output

JSON candidates include typed worktree metadata when available:

```json
{
  "path": "/worktrees/api-auth",
  "label": "api (fix/auth)",
  "source": "projects",
  "is_worktree": true,
  "branch": "fix/auth"
}
```

TSV keeps exactly three columns. The label carries the branch badge:

```text
/worktrees/api-auth	api (fix/auth) [worktree: fix/auth]	
```

Tabs and newlines in branch names are replaced with spaces, so metadata cannot
create extra TSV columns or records.

## TUI and preview

Worktree rows use the `` branch icon and show the branch next to the project
label. Searching by branch name finds the candidate. The preview Git section
adds a `[worktree: <branch>]` badge, short commit from discovery metadata, and
the normal clean or changed-file status.

## Creating a worktree from the picker

`Ctrl+N` on a row inside a Git repository (an open Herdr workspace, a
project, a zoxide entry or a configured workspace) asks for a new branch
name. Herdr then creates the worktree, in the location its own settings give
worktrees, and opens a focused workspace on it (`worktree.create`); shep
names that workspace as it names any worktree (`repo@branch` by default, or
your `workspace_name`), lays it out with the template the row resolves to,
and closes the picker. If Git or Herdr refuses (the branch already exists,
for example), the reason stays in the picker's footer and nothing is
created. The new worktree is listed by the projects source from then on.
