---
last_updated: 2026-04-17
---

# MASS Workspace Spec

The Workspace Spec declares how mass should prepare a working directory for one or more AgentRuns.
It is the authority for **workspace identity, source preparation, hook lifecycle, and host-impact boundary rules**.

## Top-Level Shape

```json
{
  "massVersion": "0.1.0",
  "metadata": {},
  "source": {},
  "hooks": {},
  "prepareTimeoutSeconds": 300
}
```

| Field | Type | Required | Default | Meaning |
|---|---|---|---|---|
| `massVersion` | string | yes | — | Spec version (SemVer, major must be 0) |
| `metadata` | object | yes | — | Workspace identity |
| `source` | object | yes | — | Where the code comes from |
| `hooks` | object | no | — | Lifecycle hooks (setup/teardown) |
| `prepareTimeoutSeconds` | int | no | `300` | Max seconds for workspace preparation (source + setup hooks). Prevents unbounded git clones or long-running setup hooks from blocking indefinitely. |

## `source`

`source` describes where the workspace comes from.

### Git source

```json
{
  "source": {
    "type": "git",
    "url": "https://github.com/user/project.git",
    "ref": "feature/auth-refactor",
    "depth": 1
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `type` | string | yes | `git` |
| `url` | string | yes | Git repository URL |
| `ref` | string | no | branch, tag, or commit SHA; default is repo default branch |
| `depth` | int | no | shallow clone depth; `0` or omitted means full clone |

Git and `emptyDir` workspaces are **agentd-managed**: mass creates them under its workspace root and may delete them during cleanup.

### `emptyDir` source

```json
{
  "source": {
    "type": "emptyDir"
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `type` | string | yes | `emptyDir` |

mass creates a new empty managed directory for the workspace.

### Local source

```json
{
  "source": {
    "type": "local",
    "path": "/home/user/project"
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `type` | string | yes | `local` |
| `path` | string | yes | absolute host path to an existing directory |

A `local` workspace is **not** created or deleted by mass. It is an attachment to an already-existing host directory.

## `hooks`

Hooks let mass run host commands around workspace lifecycle transitions.

```json
{
  "hooks": {
    "setup": [
      {
        "command": "npm",
        "args": ["install"],
        "description": "Install dependencies"
      }
    ],
    "teardown": [
      {
        "command": "docker",
        "args": ["compose", "down"],
        "description": "Stop dependent services"
      }
    ]
  }
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `command` | string | yes | executable to run |
| `args` | []string | no | command arguments |
| `description` | string | no | operator-facing description |

Hooks execute in array order with the workspace directory as `cwd`.
Any `setup` hook failure fails workspace preparation.
`teardown` hook behavior and cleanup reporting are owned by mass's workspace lifecycle contract.

## Host-Impact Boundary Rules

### 1. Local workspace attachment

`local` is the highest-trust source type because it attaches mass directly to an existing host path.
The contract is explicit:

- `path` must be an absolute host path;
- mass must validate the path exists and is a directory before registration;
- mass must canonicalize the path before treating it as the realized workspace path;
- cleanup must **not** delete that canonicalized local path;
- future policy such as allowlists or root restrictions is deployment policy, not hidden implied behavior.

This is the design-set authority for the phrase **local workspace**.

### 2. Hook execution

Workspace hooks are **host commands executed by mass**.
They are not sandboxed by the Workspace Spec.
That means:

- a hook may mutate files in the workspace;
- a hook may start or stop host-side services;
- hook failure aborts workspace preparation (for `setup` hooks) and the error is returned to the ARI caller;
- hook stdout/stderr is captured by mass but is **not** currently returned through `workspace/get` — this is a future work gap;
- hook execution happens before or after agent work, not inside an agent turn.

This is the design-set authority for the phrase **hook execution**.

### 3. Environment boundary and env precedence

The Workspace Spec does **not** define per-hook `env` fields today.
The boundary rules are therefore:

- workspace hooks run in mass's host process environment, subject to future daemon policy;
- agent process env is built from: inherited daemon/host environment as the base, plus Agent definition `env` layered on top;
- there is no AgentRun-level env override in `agentrun/create`; env is fixed by the Agent definition;
- hook environment and agent environment are different surfaces — hooks are not affected by Agent definition env.

This file owns the boundary that hook environment and agent environment are different surfaces.
It does not turn workspace preparation into a secret-distribution mechanism.

### 4. Shared workspace reuse and access

A single prepared workspace may be attached to multiple AgentRuns.
The contract is explicit:

- shared workspace means shared filesystem visibility;
- shared workspace means shared write risk unless a later feature adds stronger isolation;
- cleanup must respect reference tracking and must not delete a managed workspace while it is still attached;
- external caller policy decides whether reuse is appropriate for a given workload.

This is the design-set authority for the phrase **shared workspace**.

### 5. Cleanup ownership

Managed workspaces (`git`, `emptyDir`) are created and eventually deleted by mass under workspace-manager rules.
Unmanaged local workspaces are detached, not deleted.
Any teardown hook may still run against either kind of workspace because hooks are host-impact actions, not ownership markers.

## Lifecycle Summary

Prepare:

1. validate the Workspace Spec;
2. realize the source;
3. canonicalize the realized path;
4. run `setup` hooks;
5. register the resulting `workspaceId`, canonical path, and reference state.

Cleanup:

1. ensure reference rules allow cleanup;
2. run `teardown` hooks;
3. delete managed directories only;
4. remove runtime metadata for the workspace attachment.

## Example

```json
{
  "massVersion": "0.1.0",
  "metadata": {
    "name": "backend-service"
  },
  "source": {
    "type": "git",
    "url": "https://github.com/org/backend.git",
    "ref": "main"
  },
  "hooks": {
    "setup": [
      {
        "command": "go",
        "args": ["mod", "download"],
        "description": "Download Go modules"
      }
    ],
    "teardown": [
      {
        "command": "docker",
        "args": ["compose", "down"],
        "description": "Stop dependent services"
      }
    ]
  }
}
