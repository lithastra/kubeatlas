---
title: "[ci] e2e-openshift-local failed on {{ date | date('YYYY-MM-DD') }}"
labels: ci, openshift
---

The weekly OpenShift Local (CRC) e2e job failed.

- Workflow run: {{ env.RUN_URL }}
- Triggered by: scheduled run

Common causes worth ruling out before deeper debugging:

1. **CRC bundle drift** — Red Hat ships new CRC bundles roughly monthly; if the
   pinned `CRC_VERSION` in the workflow falls behind by a couple of versions
   the start step can hang. Bump `CRC_VERSION` in
   `.github/workflows/e2e-openshift-local.yml`.
2. **Pull-secret rotation** — if `CRC_PULL_SECRET` was rotated, regenerate
   it from <https://console.redhat.com/openshift/install/pull-secret>.
3. **Detector log missing** — the assertion looks for the literal
   `OpenShift API group detected` line. Anyone who renames that log
   should also update both the workflow and `test/verify/phase2.sh`.
4. **ROUTES_TO never lands** — usually means the openshift rule pack
   is not loading; check the `kubeatlas_rego_modules_loaded` metric
   in the uploaded `kubeatlas.log` artifact.

The workflow uploaded `crc-e2e-logs` (Pods table + kubeatlas log + describe)
to the run page; download that before diagnosing.
