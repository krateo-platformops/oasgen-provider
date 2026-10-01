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
{{- define "oasgen-provider.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "oasgen-provider.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
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

{{/*
oasgen-render names and labels. The app name differs from the manager's so neither Deployment's selector
matches the other's pods.

render.fullnameOverride, when set, replaces the generated name entirely -- for the Deployment, the Service
and the selector labels. It exists so a platform can place this behind a
naming convention of its own without forking the chart; the chart takes no view on the name.

CAUTION on the selector: a Deployment's spec.selector is IMMUTABLE. Setting, changing or removing this
value on a release that ALREADY has render enabled makes the upgrade fail with "field is immutable" --
the Deployment must be deleted first. Setting it at the same time as render.enabled=true, on a release
that has never run the render pod, is unaffected.
*/}}
{{- define "oasgen-provider.render.fullname" -}}
{{- if .Values.render.fullnameOverride }}
{{- .Values.render.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-render" (include "oasgen-provider.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "oasgen-provider.render.selectorLabels" -}}
{{- if .Values.render.fullnameOverride }}
app.kubernetes.io/name: {{ .Values.render.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
app.kubernetes.io/name: {{ printf "%s-render" (include "oasgen-provider.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "oasgen-provider.render.labels" -}}
helm.sh/chart: {{ include "oasgen-provider.chart" . }}
{{ include "oasgen-provider.render.selectorLabels" . }}
app.kubernetes.io/component: render
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
