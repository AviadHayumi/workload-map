
{{/*
Fully qualified resource name: "<chart-name>-operator" (e.g. "karta-operator").
Override wholesale via fullnameOverride.
*/}}
{{- define "karta.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-operator" .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Common labels stamped onto every resource.
*/}}
{{- define "karta.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "karta.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels. These must be stable across upgrades, so they intentionally
exclude version/chart labels (which change between releases).
*/}}
{{- define "karta.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the ServiceAccount to use. When create=true the chart owns the name
(karta.fullname). When create=false the caller must supply serviceAccount.name.
*/}}
{{- define "karta.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- include "karta.fullname" . -}}
{{- else -}}
{{- required "serviceAccount.name is required when serviceAccount.create is false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Name for the CRD upgrader hook resources. Includes the release namespace and
name so separate releases never share the cluster-scoped ClusterRole/Binding.
*/}}
{{- define "karta.crdUpgrader.name" -}}
{{- printf "%s-crd-upgrader-%s-%s" (include "karta.fullname" .) .Release.Namespace .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Webhook resource names. The service name doubles as the serving cert SAN, so it
must match the --webhook-service-name flag passed to the operator.
*/}}
{{- define "karta.webhook.serviceName" -}}
{{- printf "%s-webhook" (include "karta.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "karta.webhook.secretName" -}}
{{- printf "%s-webhook-cert" (include "karta.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "karta.webhook.configName" -}}
{{- printf "%s-mutating" (include "karta.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "karta.webhook.validatingConfigName" -}}
{{- printf "%s-validating" (include "karta.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Exporter resource names and labels. The exporter is a separate workload with
its own ServiceAccount; it never shares the operator's RBAC.
*/}}
{{- define "karta.exporter.fullname" -}}
{{- if .Values.exporter.fullnameOverride -}}
{{- .Values.exporter.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-exporter" .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "karta.exporter.labels" -}}
{{ include "karta.labels" . }}
app.kubernetes.io/component: exporter
{{- end -}}

{{- define "karta.exporter.selectorLabels" -}}
{{ include "karta.selectorLabels" . }}
app.kubernetes.io/component: exporter
{{- end -}}

{{- define "karta.exporter.serviceAccountName" -}}
{{- if .Values.exporter.serviceAccount.create -}}
{{- include "karta.exporter.fullname" . -}}
{{- else -}}
{{- required "exporter.serviceAccount.name is required when exporter.serviceAccount.create is false" .Values.exporter.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "karta.exporter.attributionEnabled" -}}
{{- if or .Values.exporter.attribution.enabled (trim .Values.exporter.attribution.submittedByExpression) -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{- define "karta.exporter.identityConfig" -}}
{{- if and .Values.exporter.attribution.submittedByExpression .Values.exporter.attribution.rules -}}
{{- fail "configure exporter.attribution.submittedByExpression or rules, not both" -}}
{{- end -}}
{{- if .Values.exporter.attribution.submittedByExpression -}}
{{- dict "submittedByExpression" .Values.exporter.attribution.submittedByExpression | toJson -}}
{{- else -}}
{{- if not .Values.exporter.attribution.rules -}}{{- fail "exporter.attribution requires submittedByExpression or rules" -}}{{- end -}}
{{- dict "rules" .Values.exporter.attribution.rules | toJson -}}
{{- end -}}
{{- end -}}

{{- define "karta.submitterAdmission" -}}
{{- if and .Values.exporter.attribution.admission.enabled .Values.submitterAdmission.enabled -}}
{{- fail "configure exporter.attribution.admission or submitterAdmission, not both" -}}
{{- end -}}
{{- if .Values.exporter.attribution.admission.enabled -}}
{{- toYaml .Values.exporter.attribution.admission -}}
{{- else -}}
{{- toYaml .Values.submitterAdmission -}}
{{- end -}}
{{- end -}}
