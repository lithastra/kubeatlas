// Copyright 2026 The KubeAtlas Authors
// SPDX-License-Identifier: Apache-2.0

package extractor

import (
	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type networkPolicySelector struct {
	matchLabels map[string]string
	expressions labels.Selector
}

func (s networkPolicySelector) matches(have map[string]string) bool {
	return matchLabelSelector(have, s.matchLabels) && s.expressions.Matches(labels.Set(have))
}

// networkPolicyLabelSelector validates the complete expression list before any
// candidate reads. Keep equality's existing shape/empty-field compatibility;
// use the pinned Kubernetes evaluator for expression syntax and matching.
// Never wrap converter errors: they can contain user-supplied keys or values.
func networkPolicyLabelSelector(raw map[string]any, path ...string) (networkPolicySelector, error) {
	var out networkPolicySelector
	labelPath := append(append([]string(nil), path...), "matchLabels")
	matchLabels, err := selectorStringMap(raw, labelPath...)
	if err != nil {
		return out, err
	}
	out.matchLabels = matchLabels
	value := nestedMap(raw, path...)["matchExpressions"]
	selector := &metav1.LabelSelector{}
	if value != nil {
		items, ok := value.([]any)
		if !ok {
			return out, errors.New("label selector expressions must be an array")
		}
		for _, item := range items {
			object, ok := item.(map[string]any)
			if !ok || object == nil {
				return out, errors.New("label selector expression must be an object")
			}
			key, keyOK := object["key"].(string)
			operator, operatorOK := object["operator"].(string)
			if !keyOK || !operatorOK {
				return out, errors.New("label selector expression key and operator must be strings")
			}
			var values []string
			if object["values"] != nil {
				list, ok := object["values"].([]any)
				if !ok {
					return out, errors.New("label selector expression values must be an array")
				}
				for _, item := range list {
					text, ok := item.(string)
					if !ok {
						return out, errors.New("label selector expression values must be strings")
					}
					values = append(values, text)
				}
			}
			selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{
				Key: key, Operator: metav1.LabelSelectorOperator(operator), Values: values,
			})
		}
	}
	out.expressions, err = metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return networkPolicySelector{}, errors.New("invalid label selector expression")
	}
	return out, nil
}
