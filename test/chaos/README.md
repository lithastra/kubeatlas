# Chaos scenarios

Four hand-runnable scripts that poke KubeAtlas with situations that
shouldn't happen in a healthy cluster but routinely do in real life.
Phase 1 v0.1.0 exit gate is "the binary survives all four without
crashing or wedging"; honest documentation of the *observed* graph
behaviour for each is captured in the script header.

These scripts are **not wired into CI**. They run against a real
kind cluster with the PetClinic phase1 fixture loaded. CI
automation lands in Phase 2.

## Prerequisites

- `kubectl` pointed at a kind cluster.
- `test/petclinic/run.sh phase1` already applied.
- `kubeatlas` running in watch mode (one terminal: `kubeatlas`).
- A second terminal to drive the chaos scripts.
- The Web UI open at `http://localhost:8080` while observing.

## Scenarios

| Script | What it does | What you should see |
|---|---|---|
| `dangling-ref.sh` | Deletes a ConfigMap that a Deployment still references. | The ConfigMap node and its `USES_CONFIGMAP` edge disappear from the Deployment's neighbour view within a few seconds. v0.1.0 does **not** mark the edge as "broken"; the cascade is total. (Phase 2 tracks `BROKEN_REF` as a first-class edge state.) |
| `owner-loop.sh` | Creates two ConfigMaps that name each other in `metadata.ownerReferences`. | The OwnerRef walker is BFS over an `owned->owner` map keyed on UID; cycles short-circuit on the visited set. KubeAtlas keeps responding; the workload aggregator does not loop. |
| `resource-storm.sh` | Creates 100 ConfigMaps in a tight loop. | The Resources page shows the new ConfigMaps within ~30 s of the last `kubectl apply`. WS subscribers receive 100 GraphUpdate envelopes; no events lost (verify with `wscat`). |
| `api-server-flap.sh` | Scales the kind apiserver to 0 then back to 1. | The informer logs a watch error, retries with exponential backoff, and re-syncs. `/readyz` flips back to 200. Web UI shows a stale graph during the outage and refreshes on reconnect. |

## How to run

```bash
# Terminal 1
kubeatlas

# Terminal 2 (after kubeatlas reports "informer caches synced")
bash test/chaos/dangling-ref.sh
bash test/chaos/owner-loop.sh
bash test/chaos/resource-storm.sh
bash test/chaos/api-server-flap.sh
```

Each script is self-cleaning by default. Pass `--no-cleanup` to keep
the chaos in place for further inspection.

## Reporting back

Anything that diverges from the table above is interesting. Open an
issue at <https://github.com/lithastra/kubeatlas/issues> with:

1. The chaos scenario name.
2. Cluster details (`kubectl version --short`, distro).
3. KubeAtlas version (`kubeatlas -version`).
4. Observed behaviour vs expected.
5. The relevant chunk of `kubeatlas.log`.
