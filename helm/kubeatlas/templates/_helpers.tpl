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

{{/*
Postgres connection env block, shared by the wait-for-pg init
container and the kubeatlas main container. Branches on embedded vs
BYO so each path renders the right host/secret reference:

  - embedded.enabled=true: host = "<release>-<suffix>-rw" (CNPG's
    read-write Service); password from CNPG-managed app Secret
    "<release>-<suffix>-app".
  - BYO: host/port/db/user from values.persistence.connection;
    password from passwordSecretRef if set (preferred for prod),
    else the plaintext .Values.persistence.connection.password.
*/}}
{{- define "kubeatlas.pgEnv" -}}
{{- $clusterName := printf "%s-%s" (include "kubeatlas.fullname" .) .Values.persistence.embedded.clusterNameSuffix -}}
{{- if .Values.persistence.embedded.enabled }}
- name: PGHOST
  value: {{ printf "%s-rw" $clusterName | quote }}
- name: PGPORT
  value: "5432"
- name: PGDATABASE
  value: {{ .Values.persistence.connection.database | quote }}
- name: PGUSER
  value: {{ .Values.persistence.connection.user | quote }}
- name: PGPASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ printf "%s-app" $clusterName | quote }}
      key: password
- name: PGSSLMODE
  value: {{ .Values.persistence.connection.sslMode | quote }}
{{- else }}
- name: PGHOST
  value: {{ .Values.persistence.connection.host | quote }}
- name: PGPORT
  value: {{ .Values.persistence.connection.port | quote }}
- name: PGDATABASE
  value: {{ .Values.persistence.connection.database | quote }}
- name: PGUSER
  value: {{ .Values.persistence.connection.user | quote }}
{{- if .Values.persistence.connection.passwordSecretRef.name }}
- name: PGPASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.persistence.connection.passwordSecretRef.name | quote }}
      key: {{ .Values.persistence.connection.passwordSecretRef.key | quote }}
{{- else }}
- name: PGPASSWORD
  value: {{ .Values.persistence.connection.password | quote }}
{{- end }}
- name: PGSSLMODE
  value: {{ .Values.persistence.connection.sslMode | quote }}
{{- end }}
{{- end -}}
