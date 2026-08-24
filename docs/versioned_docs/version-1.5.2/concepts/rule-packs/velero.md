---
sidebar_position: 6
title: Velero
---

# Velero rule pack

The `velero` pack models the relationships the `velero.io` API
group adds between backups, restores, schedules and their storage
locations.

- **OCI artifact:** `oci://ghcr.io/lithastra/rules/velero:0.1.0`
- **Requires:** KubeAtlas ≥ 1.1.0

## Modules

| Module | Matched kind | Edges |
|--------|--------------|-------|
| `backup` | `Backup` | `STORED_IN`, `USES_SNAPSHOT_LOCATION` |
| `restore` | `Restore` | `RESTORES_FROM` |
| `schedule` | `Schedule` | `STORED_IN`, `USES_SNAPSHOT_LOCATION` |

## Edges

- **`STORED_IN`** — a Backup to the BackupStorageLocation named by
  `spec.storageLocation`.
- **`USES_SNAPSHOT_LOCATION`** — a Backup to each
  VolumeSnapshotLocation in `spec.volumeSnapshotLocations[]`.
- **`RESTORES_FROM`** — a Restore to the Backup named by
  `spec.backupName`, or the Schedule named by `spec.scheduleName`.
- A Schedule derives the two Backup edges from its embedded
  `spec.template`.

Velero installs every CR into one namespace, so each referenced
resource is resolved in the referencing resource's namespace.

## Out of scope

The object-storage bucket or snapshot provider behind a storage
location — that is cloud infrastructure, not a cluster resource;
internal CRs (DownloadRequest, PodVolumeBackup, DataUpload); backup
label selectors and `includedNamespaces`.

## Try it

```bash
kubeatlas rules-test --pack=oci://ghcr.io/lithastra/rules/velero:0.1.0
```
