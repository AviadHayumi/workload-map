// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

//go:build js && wasm

package main

import (
	"syscall/js"
)

func main() {
	registerKartaAPI()

	select {}
}

func registerKartaAPI() {
	kartaAPI := js.Global().Get("Object").New()
	kartaAPI.Set("buildTree", js.FuncOf(jsBuildTree))
	kartaAPI.Set("rootInstances", js.FuncOf(jsRootInstances))
	kartaAPI.Set("evaluatePhases", js.FuncOf(jsEvaluatePhases))
	kartaAPI.Set("referenceQueries", js.FuncOf(jsReferenceQueries))
	kartaAPI.Set("listCatalog", js.FuncOf(jsListCatalog))
	js.Global().Set("karta", kartaAPI)
}
