{{/*
Expand the name of the chart.
*/}}
{{- define "metric-cacher-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "metric-cacher-exporter.fullname" -}}
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
{{- define "metric-cacher-exporter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "metric-cacher-exporter.labels" -}}
helm.sh/chart: {{ include "metric-cacher-exporter.chart" . }}
{{ include "metric-cacher-exporter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "metric-cacher-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "metric-cacher-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "metric-cacher-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "metric-cacher-exporter.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Get redis service name
*/}}
{{- define "metric-cacher-exporter.redis.serviceName" -}}
{{- if .Values.redis.enabled -}}
    {{- if and (eq .Values.redis.architecture "standalone") .Values.redis.sentinel.enabled -}}
        {{- printf "%s-%s" .Release.Name (default "redis" .Values.redis.nameOverride | trunc 63 | trimSuffix "-") -}}
    {{- else -}}
        {{- printf "%s-%s-master" .Release.Name (default "redis" .Values.redis.nameOverride | trunc 63 | trimSuffix "-") -}}
    {{- end -}}
{{- else -}}
    {{- .Values.config.redis.addr | default "redis-master" | splitList ":" | first -}}
{{- end -}}
{{- end -}}

{{/*
Get redis service port
*/}}
{{- define "metric-cacher-exporter.redis.port" -}}
{{- if .Values.redis.enabled -}}
    {{- if .Values.redis.sentinel.enabled -}}
        {{- .Values.redis.sentinel.service.ports.sentinel -}}
    {{- else -}}
        {{- .Values.redis.master.service.ports.redis -}}
    {{- end -}}
{{- else -}}
    {{- $addr := .Values.config.redis.addr | default "redis-master:6379" -}}
    {{- if contains ":" $addr -}}
        {{- $addr | splitList ":" | last -}}
    {{- else -}}
        6379
    {{- end -}}
{{- end -}}
{{- end -}}

{{/*
Get redis secret name
*/}}
{{- define "metric-cacher-exporter.redis.secretName" -}}
{{- if .Values.redis.enabled -}}
    {{- if .Values.redis.auth.existingSecret -}}
        {{- .Values.redis.auth.existingSecret -}}
    {{- else -}}
        {{- printf "%s-%s" .Release.Name (default "redis" .Values.redis.nameOverride | trunc 63 | trimSuffix "-") -}}
    {{- end -}}
{{- else -}}
    {{- .Values.config.redis.existingSecret -}}
{{- end -}}
{{- end -}}

{{/*
Get redis secret password key
*/}}
{{- define "metric-cacher-exporter.redis.secretPasswordKey" -}}
{{- if .Values.redis.enabled -}}
    {{- .Values.redis.auth.existingSecretPasswordKey | default "redis-password" -}}
{{- else -}}
    {{- .Values.config.redis.existingSecretPasswordKey | default "redis-password" -}}
{{- end -}}
{{- end -}}