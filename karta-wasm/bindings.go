// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"errors"
	"syscall/js"

	"github.com/dsx-ai-factory/workload-map/karta-wasm/core"
	"github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/resource"
)

func jsBuildTree(_ js.Value, args []js.Value) any {
	return readWorkload(args, core.BuildTree)
}

func jsRootInstances(_ js.Value, args []js.Value) any {
	return readWorkload(args, core.RootInstances)
}

func jsEvaluatePhases(_ js.Value, args []js.Value) any {
	return readWorkload(args, core.EvaluatePhases)
}

func readWorkload[T any](args []js.Value, read func(context.Context, string, string, ...resource.FactoryOption) (T, error)) js.Value {
	if len(args) < 2 || len(args) > 3 {
		return encodeEnvelope(nil, errors.New("expected definition, workload and optional reference options"))
	}
	var opts []resource.FactoryOption
	if len(args) == 3 {
		var input core.ReadOptions
		if err := json.Unmarshal([]byte(args[2].String()), &input); err != nil {
			return encodeEnvelope(nil, errors.New("invalid reference options"))
		}
		if input.ReferencesEnabled != nil {
			opts = append(opts, resource.WithReferencesEnabled(*input.ReferencesEnabled))
		}
		if input.References != nil {
			opts = append(opts, resource.WithReferences(input.References))
		}
	}
	result, err := read(context.Background(), args[0].String(), args[1].String(), opts...)
	return encodeEnvelope(result, err)
}

func jsReferenceQueries(_ js.Value, args []js.Value) any {
	if len(args) != 2 {
		return encodeEnvelope(nil, errors.New("expected definition and workload"))
	}
	result, err := core.ReferenceQueries(context.Background(), args[0].String(), args[1].String())
	return encodeEnvelope(result, err)
}

func jsListCatalog(js.Value, []js.Value) any {
	return encodeEnvelope(core.ListCatalog(), nil)
}
