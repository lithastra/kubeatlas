{{/*
Expand the name of the chart.
*/}}
{{- define "kubeatlas.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Build a fully qualified app name capped at 63 chars (k8s name limit).
*/}}
{{- define "kubeatlas.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart name + version label per Helm convention.
*/}}
{{- define "kubeatlas.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels emitted on every rendered object.
*/}}
{{- define "kubeatlas.labels" -}}
helm.sh/chart: {{ include "kubeatlas.chart" . }}
{{ include "kubeatlas.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.extraLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
Selector labels — used by Service.spec.selector and Deployment.spec.selector.
Stable across versions; Deployment selectors are immutable.
*/}}
{{- define "kubeatlas.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubeatlas.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Effective ServiceAccount name: explicit override or computed default.
*/}}
{{- define "kubeatlas.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kubeatlas.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Effective container image reference. Falls back to .Chart.AppVersion
when .Values.image.tag is empty.
*/}}
{{- define "kubeatlas.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
