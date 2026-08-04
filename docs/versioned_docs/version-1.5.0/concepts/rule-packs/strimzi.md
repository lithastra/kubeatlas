---
sidebar_position: 5
title: Strimzi
---

# Strimzi rule pack

The `strimzi` pack models the cluster-membership and workload
relationships the `kafka.strimzi.io` API group adds.

- **OCI artifact:** `oci://ghcr.io/lithastra/rules/strimzi:0.1.0`
- **Requires:** KubeAtlas ≥ 1.1.0

## Modules

| Module | Matched kind | Edges |
|--------|--------------|-------|
| `kafka` | `Kafka` | `MANAGES` |
| `kafka-topic` | `KafkaTopic` | `BELONGS_TO_CLUSTER` |
| `kafka-user` | `KafkaUser` | `BELONGS_TO_CLUSTER` |

## Edges

- **`MANAGES`** — a ZooKeeper-based Kafka cluster (one carrying a
  `spec.zookeeper` block) to the StatefulSets the operator names
  `<cluster>-kafka` and `<cluster>-zookeeper`. KRaft clusters carry
  no `spec.zookeeper` and derive no edge.
- **`BELONGS_TO_CLUSTER`** — a KafkaTopic or KafkaUser to the Kafka
  cluster named by its required `strimzi.io/cluster` label.

## Out of scope

KRaft and KafkaNodePool topology — node pools and StrimziPodSets
vary by Strimzi version, so v0.1 covers the classic ZooKeeper
StatefulSet pair only; KafkaConnect / KafkaConnector / KafkaBridge
/ KafkaMirrorMaker; listener TLS Secrets and the per-user
credential Secret.

## Try it

```bash
kubeatlas rules-test --pack=oci://ghcr.io/lithastra/rules/strimzi:0.1.0
```
