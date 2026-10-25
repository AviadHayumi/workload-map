<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright (c) 2026 NVIDIA Corporation -->

# OTel Collector

`Registrar.Register` is the registration contract: an OTel Meter, a Store `Capturer`
and phase options produce a `metric.Registration`. Nil `WorkloadPhases` selects
all phases. The generated Registrar mock lets startup be tested independently.

The implementation will register one callback for the four KEP metric families.
Each collection captures Store once, checks evidence validity and emits saved
values. It does not read Kubernetes or run expressions.

The command owns the reader, HTTP endpoint and provider shutdown. It unregisters
the callback after Runtime stops, before shutting down the provider.

No instruments or collection callback are implemented here. The scaffold uses
a no-op meter and registers no instruments.
