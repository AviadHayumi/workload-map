// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

// Package association evaluates captured metadata without changing Karta roots.
package association

import "fmt"

type Capture struct {
	Pods       []Object             `json:"pods"`
	TargetSets map[string]TargetSet `json:"targetSets,omitempty"`
}

type Object struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
}

type Metadata struct {
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	UID         string            `json:"uid"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type Rule struct {
	Relation       string `json:"relation"`
	Source         Field  `json:"source"`
	TargetSet      string `json:"targetSet,omitempty"`
	MatchAttribute string `json:"matchAttribute,omitempty"`
}

type Field struct {
	From string `json:"from"`
	Key  string `json:"key"`
}

// Complete certifies the candidate set for this capture's namespaces. A failed
// or partial read cannot certify absence.
type TargetSet struct {
	Complete bool     `json:"complete"`
	Error    string   `json:"error,omitempty"`
	Targets  []Target `json:"targets"`
}

// ID is a logical identity within the named set. Kubernetes is present only for
// an observed API object, with its real UID. Logical IDs never become UIDs.
type Target struct {
	Namespace  string            `json:"namespace"`
	ID         string            `json:"id"`
	Attributes map[string]string `json:"attributes"`
	Kubernetes *Object           `json:"kubernetes,omitempty"`
}

type Result struct {
	Pod        Object  `json:"pod"`
	Relation   string  `json:"relation"`
	Value      string  `json:"value,omitempty"`
	Status     Status  `json:"status"`
	Target     *Target `json:"target,omitempty"`
	Error      string  `json:"error,omitempty"`
	Candidates int     `json:"candidates,omitempty"`
}

type Status string

const (
	ObservedMetadata Status = "observed_metadata"
	Resolved         Status = "resolved_target"
	Absent           Status = "absent"
	Ambiguous        Status = "ambiguous"
	Failed           Status = "error"
)

// Evaluate processes one complete Pod snapshot. No previous rows survive a call.
// Target matches must be unique in the Pod namespace.
func Evaluate(capture Capture, rules []Rule) ([]Result, error) {
	seenRules := make(map[string]bool)
	for _, rule := range rules {
		if rule.Relation == "" || seenRules[rule.Relation] {
			return nil, fmt.Errorf("relation must be nonempty and unique: %q", rule.Relation)
		}
		seenRules[rule.Relation] = true
		if rule.Source.Key == "" || (rule.Source.From != "label" && rule.Source.From != "annotation") {
			return nil, fmt.Errorf("relation %q: source must name a label or annotation", rule.Relation)
		}
		if (rule.TargetSet == "") != (rule.MatchAttribute == "") {
			return nil, fmt.Errorf("relation %q: targetSet and matchAttribute must appear together", rule.Relation)
		}
	}
	results := make([]Result, 0, len(capture.Pods)*len(rules))
	seenPods := make(map[string]bool)
	for _, pod := range capture.Pods {
		if pod.APIVersion != "v1" || pod.Kind != "Pod" || pod.Metadata.Namespace == "" || pod.Metadata.Name == "" || pod.Metadata.UID == "" {
			return nil, fmt.Errorf("captured Pod requires v1/Pod and observed namespace, name and UID")
		}
		if seenPods[pod.Metadata.UID] {
			return nil, fmt.Errorf("duplicate captured Pod UID %q", pod.Metadata.UID)
		}
		seenPods[pod.Metadata.UID] = true
		for _, rule := range rules {
			ref := pod
			ref.Metadata.Labels, ref.Metadata.Annotations = nil, nil
			result := Result{Pod: ref, Relation: rule.Relation, Status: Absent}
			if rule.Source.From == "label" {
				result.Value = pod.Metadata.Labels[rule.Source.Key]
			} else {
				result.Value = pod.Metadata.Annotations[rule.Source.Key]
			}
			switch {
			case result.Value == "":
			case rule.TargetSet == "":
				result.Status = ObservedMetadata
			default:
				set, exists := capture.TargetSets[rule.TargetSet]
				switch {
				case !exists:
					result.Status, result.Error = Failed, "target set was not supplied"
				case set.Error != "":
					result.Status, result.Error = Failed, set.Error
				case !set.Complete:
					result.Status, result.Error = Failed, "target set is incomplete"
				default:
					for _, target := range set.Targets {
						if target.Namespace != pod.Metadata.Namespace || target.Attributes[rule.MatchAttribute] != result.Value {
							continue
						}
						if target.ID == "" || target.Kubernetes != nil && (target.Kubernetes.APIVersion == "" || target.Kubernetes.Kind == "" || target.Kubernetes.Metadata.UID == "" || target.Kubernetes.Metadata.Name == "" || target.Kubernetes.Metadata.Namespace != target.Namespace) {
							result.Status, result.Error = Failed, "matching target has incomplete identity"
							break
						}
						result.Candidates++
						result.Target = &target
					}
					if result.Status != Failed {
						switch result.Candidates {
						case 0:
						case 1:
							result.Status = Resolved
						default:
							result.Status = Ambiguous
						}
					}
					if result.Status != Resolved {
						result.Target = nil
					}
				}
			}
			results = append(results, result)
		}
	}
	return results, nil
}
