// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/dsx-ai-factory/workload-map/pkg/api/runai/v1alpha1"
	"github.com/dsx-ai-factory/workload-map/pkg/jq/execution"
	"github.com/dsx-ai-factory/workload-map/pkg/resource"
)

// probeValue is what every change probe writes, so the changed leaf is
// recognized by its value wherever the definition's path put it.
const probeValue = "karta-verify-probe"

// change is one leaf that differs between the object before and after a write.
type change struct {
	path          string
	before, after any
}

// writeProbe is one round trip through the definition's write paths: what it
// changed, and the warning it earned if the change was not the expected one.
type writeProbe struct {
	component string
	name      string
	changes   []change
	warning   string
}

var (
	errNoInstances  = errors.New("no instances extracted, nothing to write")
	errNoProbeField = errors.New("no schedulerName, labels or image path to probe")

	identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	plainPathRe  = regexp.MustCompile(`^(\.[A-Za-z_][A-Za-z0-9_-]*|\.?\["[^"]+"\]|\[[0-9]+\])+$`)
	pathTokenRe  = regexp.MustCompile(`\.[A-Za-z_][A-Za-z0-9_-]*|\.?\["[^"]+"\]|\[[0-9]+\]`)
)

// probeWrites round-trips every write path of every component in memory, each
// on its own copy of the object. The identity write (read a pod spec, write it
// back unchanged) must change nothing. The change probe (one field set to
// probeValue) must change one leaf per instance and nothing else. Suspend and
// resume must change only their action paths.
func probeWrites(ctx context.Context, karta *v1alpha1.Karta, obj *unstructured.Unstructured) []writeProbe {
	var probes []writeProbe
	defs := append([]v1alpha1.ComponentDefinition{karta.Spec.StructureDefinition.RootComponent}, karta.Spec.StructureDefinition.ChildComponents...)
	for _, def := range defs {
		if def.SpecDefinition != nil {
			probes = append(probes, probeSpec(ctx, def, obj)...)
		}
		if def.SuspendDefinition != nil {
			probes = append(probes, probeSuspend(ctx, def, obj)...)
		}
	}
	return probes
}

func probeSpec(ctx context.Context, def v1alpha1.ComponentDefinition, obj *unstructured.Unstructured) []writeProbe {
	identity := writeProbe{component: def.Name, name: "identity write"}
	probe := writeProbe{component: def.Name, name: "change probe"}
	instances := 0
	run := func(p *writeProbe, mutate bool) error {
		copied := obj.DeepCopy()
		acc := resource.NewAccessor(execution.NewDefaultRunner(copied))
		n, err := writeSpec(ctx, acc, def, mutate)
		if err != nil {
			return err
		}
		instances = n
		after, err := acc.GetObject()
		if err != nil {
			return err
		}
		p.changes = diffObjects(obj.Object, after)
		return nil
	}

	switch err := run(&identity, false); {
	case errors.Is(err, errNoInstances):
		identity.name += " (skipped: " + err.Error() + ")"
		probe.name += " (skipped: " + errNoInstances.Error() + ")"
		return []writeProbe{identity, probe}
	case err != nil:
		identity.warning = fmt.Sprintf("%s identity write: %v", def.Name, err)
	case len(identity.changes) > 0:
		identity.warning = fmt.Sprintf("%s identity write changed %d path(s); writing a pod spec back unchanged must change nothing", def.Name, len(identity.changes))
	}

	switch err := run(&probe, true); {
	case errors.Is(err, errNoProbeField):
		probe.name += " (skipped: " + err.Error() + ")"
	case err != nil:
		probe.warning = fmt.Sprintf("%s change probe: %v", def.Name, err)
	default:
		if bad := unexpectedChanges(probe.changes, instances); bad != "" {
			probe.warning = fmt.Sprintf("%s change probe: %s", def.Name, bad)
		}
	}
	return []writeProbe{identity, probe}
}

// writeSpec reads the component's pod spec through the definition and writes it
// back, with one probe field set when mutate is true. It returns the number of
// instances written.
func writeSpec(ctx context.Context, acc *resource.Accessor, def v1alpha1.ComponentDefinition, mutate bool) (int, error) {
	spec := def.SpecDefinition
	switch {
	case spec.PodTemplateSpecPath != nil:
		specs, err := acc.ExtractPodTemplateSpec(ctx, def)
		if err != nil {
			return 0, err
		}
		if len(specs) == 0 {
			return 0, errNoInstances
		}
		if mutate {
			for i := range specs {
				setNodeSelectorProbe(&specs[i].Spec)
			}
		}
		return len(specs), acc.UpdatePodTemplateSpec(ctx, def, specs)
	case spec.PodSpecPath != nil:
		specs, err := acc.ExtractPodSpec(ctx, def)
		if err != nil {
			return 0, err
		}
		if len(specs) == 0 {
			return 0, errNoInstances
		}
		if mutate {
			for i := range specs {
				setNodeSelectorProbe(&specs[i])
			}
		}
		if err := acc.UpdatePodSpec(ctx, def, specs); err != nil {
			return 0, err
		}
		if spec.MetadataPath != nil {
			metas, err := acc.ExtractPodMetadata(ctx, def)
			if err != nil {
				return 0, err
			}
			if len(metas) > 0 {
				if err := acc.UpdatePodMetadata(ctx, def, metas); err != nil {
					return 0, err
				}
			}
		}
		return len(specs), nil
	case spec.FragmentedPodSpecDefinition != nil:
		specs, err := acc.ExtractFragmentedPodSpec(ctx, def)
		if err != nil {
			return 0, err
		}
		if len(specs) == 0 {
			return 0, errNoInstances
		}
		if mutate {
			frag := spec.FragmentedPodSpecDefinition
			for i := range specs {
				switch {
				case frag.SchedulerNamePath != nil:
					specs[i].SchedulerName = probeValue
				case frag.LabelsPath != nil:
					if specs[i].Labels == nil {
						specs[i].Labels = map[string]string{}
					}
					specs[i].Labels["karta-verify/probe"] = probeValue
				case frag.ImagePath != nil:
					specs[i].Image = probeValue
				default:
					return 0, errNoProbeField
				}
			}
		}
		return len(specs), acc.UpdateFragmentedPodSpec(ctx, def, specs)
	}
	return 0, errNoInstances
}

func setNodeSelectorProbe(spec *corev1.PodSpec) {
	if spec.NodeSelector == nil {
		spec.NodeSelector = map[string]string{}
	}
	spec.NodeSelector["karta-verify/probe"] = probeValue
}

// unexpectedChanges explains what a change probe altered beyond one probe leaf
// per instance, or returns "" when the diff is exactly that.
func unexpectedChanges(changes []change, instances int) string {
	probed := 0
	var other []string
	for _, c := range changes {
		if s, ok := c.after.(string); ok && s == probeValue {
			probed++
			continue
		}
		other = append(other, c.path)
	}
	switch {
	case len(other) > 0:
		return fmt.Sprintf("%d path(s) changed besides the probe field: %s", len(other), strings.Join(other, ", "))
	case probed != instances:
		return fmt.Sprintf("the probe field landed in %d place(s) for %d instance(s)", probed, instances)
	}
	return ""
}

func probeSuspend(ctx context.Context, def v1alpha1.ComponentDefinition, obj *unstructured.Unstructured) []writeProbe {
	copied := obj.DeepCopy()
	acc := resource.NewAccessor(execution.NewDefaultRunner(copied))
	suspend := writeProbe{component: def.Name, name: "suspend"}
	resume := writeProbe{component: def.Name, name: "resume"}
	allowed := actionPaths(def.SuspendDefinition)

	if err := acc.ApplySuspendActions(ctx, def); err != nil {
		suspend.warning = fmt.Sprintf("%s suspend: %v", def.Name, err)
		return []writeProbe{suspend}
	}
	after, err := acc.GetObject()
	if err != nil {
		suspend.warning = fmt.Sprintf("%s suspend: %v", def.Name, err)
		return []writeProbe{suspend}
	}
	suspend.changes = diffObjects(obj.Object, after)
	if bad := outsidePaths(suspend.changes, allowed); bad != "" {
		suspend.warning = fmt.Sprintf("%s suspend changed paths outside its actions: %s", def.Name, bad)
	}

	if err := acc.ApplyResumeActions(ctx, def); err != nil {
		resume.warning = fmt.Sprintf("%s resume: %v", def.Name, err)
		return []writeProbe{suspend, resume}
	}
	after, err = acc.GetObject()
	if err != nil {
		resume.warning = fmt.Sprintf("%s resume: %v", def.Name, err)
		return []writeProbe{suspend, resume}
	}
	resume.changes = diffObjects(obj.Object, after)
	if bad := outsidePaths(resume.changes, allowed); bad != "" {
		resume.warning = fmt.Sprintf("%s resume changed paths outside its actions: %s", def.Name, bad)
	}
	return []writeProbe{suspend, resume}
}

// actionPaths returns the suspend and resume action paths in the diff's path
// form, or nil when an action is not a plain path and cannot be compared.
func actionPaths(def *v1alpha1.SuspendDefinition) map[string]bool {
	paths := map[string]bool{}
	for _, action := range append(append([]v1alpha1.SuspendAction{}, def.SuspendActions...), def.ResumeActions...) {
		if !plainPathRe.MatchString(action.Path) {
			return nil
		}
		var b strings.Builder
		for _, token := range pathTokenRe.FindAllString(action.Path, -1) {
			switch {
			case strings.HasPrefix(token, `["`) || strings.HasPrefix(token, `.["`):
				b.WriteString(joinKey("", strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(token, "."), `["`), `"]`)))
			default:
				b.WriteString(token)
			}
		}
		paths[b.String()] = true
	}
	return paths
}

// outsidePaths lists the changed paths that are not action paths, or returns ""
// when every change is on an action path or the actions cannot be compared.
func outsidePaths(changes []change, allowed map[string]bool) string {
	if allowed == nil {
		return ""
	}
	var outside []string
	for _, c := range changes {
		if !allowed[c.path] {
			outside = append(outside, c.path)
		}
	}
	return strings.Join(outside, ", ")
}

func diffObjects(before, after map[string]any) []change {
	var out []change
	diffValues("", before, after, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// diffValues descends into a container that appears or disappears so each leaf
// is one change, except an empty one, which is itself the change: a `{}` the
// object never had is exactly what a write must not add.
func diffValues(path string, a, b any, out *[]change) {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if a == nil && bIsMap && len(bm) > 0 {
		am, aIsMap = map[string]any{}, true
	}
	if b == nil && aIsMap && len(am) > 0 {
		bm, bIsMap = map[string]any{}, true
	}
	if aIsMap && bIsMap {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			diffValues(joinKey(path, k), am[k], bm[k], out)
		}
		return
	}
	as, aIsSlice := a.([]any)
	bs, bIsSlice := b.([]any)
	if a == nil && bIsSlice && len(bs) > 0 {
		as, aIsSlice = []any{}, true
	}
	if b == nil && aIsSlice && len(as) > 0 {
		bs, bIsSlice = []any{}, true
	}
	if aIsSlice && bIsSlice {
		for i := 0; i < len(as) || i < len(bs); i++ {
			var x, y any
			if i < len(as) {
				x = as[i]
			}
			if i < len(bs) {
				y = bs[i]
			}
			diffValues(fmt.Sprintf("%s[%d]", path, i), x, y, out)
		}
		return
	}
	if equalLeaf(a, b) {
		return
	}
	*out = append(*out, change{path: path, before: a, after: b})
}

// equalLeaf compares two leaves, treating every numeric type as one number so
// an int written by jq equals the float64 the YAML parser produced.
func equalLeaf(a, b any) bool {
	if fa, ok := asFloat(a); ok {
		if fb, ok := asFloat(b); ok {
			return fa == fb
		}
	}
	return reflect.DeepEqual(a, b)
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func joinKey(path, key string) string {
	if identifierRe.MatchString(key) {
		return path + "." + key
	}
	return path + `["` + key + `"]`
}

func formatLeaf(v any) string {
	if v == nil {
		return "<absent>"
	}
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(out)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}
