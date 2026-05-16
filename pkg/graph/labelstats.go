// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package graph

import "sort"

// FoldLabelStats turns a raw key -> []LabelValue map into the sorted,
// capped []LabelStat that GraphStore.LabelStats returns (F-114).
//
// Both store backends collect raw (key, value, count) tallies their
// own way — a GROUP BY on Tier 2, a Go map on Tier 1 — then hand the
// result here so the sort order and the MaxLabelValuesPerKey cap are
// defined in exactly one place and the two tiers cannot drift.
//
// Ordering: keys ascending; within a key, values by count descending
// then value ascending (so the result is deterministic across runs).
// The input slices may be mutated and are not retained by the caller.
func FoldLabelStats(byKey map[string][]LabelValue) []LabelStat {
	out := make([]LabelStat, 0, len(byKey))
	for key, vals := range byKey {
		sort.Slice(vals, func(i, j int) bool {
			if vals[i].Count != vals[j].Count {
				return vals[i].Count > vals[j].Count
			}
			return vals[i].Value < vals[j].Value
		})
		stat := LabelStat{Key: key, ValueCount: len(vals)}
		for _, v := range vals {
			stat.ResourceCount += v.Count
		}
		if len(vals) > MaxLabelValuesPerKey {
			vals = vals[:MaxLabelValuesPerKey]
		}
		stat.Values = vals
		out = append(out, stat)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
