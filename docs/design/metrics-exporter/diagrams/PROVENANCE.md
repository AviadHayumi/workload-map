<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# Diagram sources

These diagrams illustrate the implementation. They are not runtime traces.

- [Component flow](../exporter-component-flow.excalidraw).
- [Ownership traversal](../ownership-traversal.excalidraw).
- [JobSet sequence](jobset-event-flow.puml). Its PlantUML style is embedded.

The JobSet catalog already contains the component, instance and replica selectors
shown in the sequence.

The component diagram embeds the unchanged Kubernetes icon, scaled to fit.
Source: [Kubernetes website asset](https://github.com/kubernetes/website/blob/660e3dfd39e5046a5266439683f57f1bdd209fdf/static/images/kubernetes-512x512.png).
Attribution: Kubernetes Authors.
The source repository uses [CC BY 4.0](./kubernetes-website-LICENSE.txt).
Kubernetes trademarks remain with their respective owners.

The zoomable HTML embeds the sequence SVG and needs no external assets.
