---
sidebar_position: 7
title: Tekton
---

# Tekton rule pack

The `tekton` pack models the composition and execution
relationships the `tekton.dev` API group adds.

- **OCI artifact:** `oci://ghcr.io/lithastra/rules/tekton:0.1.0`
- **Requires:** KubeAtlas ≥ 1.1.0

## Modules

| Module | Matched kind | Edges |
|--------|--------------|-------|
| `pipeline` | `Pipeline` | `USES_TASK` |
| `pipelinerun` | `PipelineRun` | `RUNS_PIPELINE` |
| `taskrun` | `TaskRun` | `RUNS_TASK` |

## Edges

- **`USES_TASK`** — a Pipeline to each Task referenced by a
  `spec.tasks[]` or `spec.finally[]` `taskRef`.
- **`RUNS_PIPELINE`** — a PipelineRun to the Pipeline named by
  `spec.pipelineRef`.
- **`RUNS_TASK`** — a TaskRun to the Task named by `spec.taskRef`.

A `taskRef` of kind `ClusterTask` resolves to a cluster-scoped
`ClusterTask` node; otherwise it is a namespaced `Task` in the
referencing resource's namespace.

## Out of scope

Inline `taskSpec` / `pipelineSpec` definitions (nothing to
reference); resolver references (git / bundle / hub / cluster) that
carry no name; workspaces, params and the `runAfter` ordering DAG;
Tekton Triggers and Tekton Chains.

## Try it

```bash
kubeatlas rules-test --pack=oci://ghcr.io/lithastra/rules/tekton:0.1.0
```
