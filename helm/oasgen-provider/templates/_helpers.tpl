{{/*
Expand the name of the chart.
*/}}
{{- define "oasgen-provider.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "oasgen-provider.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "oasgen-provider.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "oasgen-provider.labels" -}}
helm.sh/chart: {{ include "oasgen-provider.chart" . }}
{{ include "oasgen-provider.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "oasgen-provider.selectorLabels" -}}
app.kubernetes.io/name: {{ include "oasgen-provider.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{/*
REFUSES create=false WITH AN EMPTY NAME (#785), and that is the whole reason this helper is not the
unmodified Helm scaffold.

The scaffold's `default "default"` fallback is harmless for a chart whose bindings are narrow. It is not
harmless for this one: with create=false and no name, ALL THREE of this chart's bindings attach to the
namespace's `default` ServiceAccount -- the one every pod gets when it names none -- and between them they
grant secrets, roles/rolebindings and customresourcedefinitions. Any workload scheduled in that namespace
without an explicit serviceAccountName inherits the lot.

WHY IT SURVIVED THE INVESTIGATION THAT LOOKED STRAIGHT AT IT. clusterrolebinding.yaml documents a
DIFFERENT problem on this same values key (#717): the binding subject and the Deployment's
serviceAccountName could DIVERGE, 403ing every call. That one fails loudly, and #717 was hunting a
mismatch. This is a MATCH -- the Deployment also runs as `default` -- so the provider works perfectly and
nothing surfaces. The grant simply reaches further than the operator asked for.

A REFUSAL AND NOT A WARNING, because the failure mode is invisible: there is no error to read, no
condition to see, and the extra grant is found by whoever audits RBAC next, or by whoever uses it. A
template that renders is a template nobody re-reads.

Found because a consumer added "create=false with an empty name, render only" to a validation harness and
mentioned authn had added a refusal for the same case (authn#164) -- so this is matching a precedent
rather than inventing a policy.
*/}}
{{- define "oasgen-provider.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "oasgen-provider.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- if not .Values.serviceAccount.name }}
{{- fail "serviceAccount.create is false but serviceAccount.name is empty. This chart's bindings would attach to the namespace's `default` ServiceAccount, granting secrets, roles/rolebindings and customresourcedefinitions to every pod there that names no ServiceAccount. Set serviceAccount.name to an existing ServiceAccount, or leave serviceAccount.create true." }}
{{- end }}
{{- .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image reference honoring an optional global.imageRegistry override (host-only; repository paths
preserved). Canonical helper — identical across all Krateo charts.
*/}}
{{- define "krateo.image" -}}
{{- $g := .global | default dict -}}
{{- $registry := $g.imageRegistry | default .img.registry -}}
{{- $tag := .img.tag | default .defaultTag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry .img.repository $tag -}}
{{- else -}}
{{- printf "%s:%s" .img.repository $tag -}}
{{- end -}}
{{- end -}}
