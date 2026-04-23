{{/*
Expand the name of the chart.
*/}}
{{- define "eks-ami-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
Truncated at 63 chars because some Kubernetes name fields are limited to this (DNS naming spec).
If the release name already contains the chart name it is used as-is.
*/}}
{{- define "eks-ami-operator.fullname" -}}
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
{{- define "eks-ami-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to all resources.
*/}}
{{- define "eks-ami-operator.labels" -}}
helm.sh/chart: {{ include "eks-ami-operator.chart" . }}
{{ include "eks-ami-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels used by Deployment and Service.
*/}}
{{- define "eks-ami-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "eks-ami-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Resolve the ServiceAccount name to use.
*/}}
{{- define "eks-ami-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "eks-ami-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Resolve the container image reference (repository:tag).
Falls back to the chart appVersion when tag is not set.
*/}}
{{- define "eks-ami-operator.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
Resolve the name of the Secret that holds webhook TLS certificates.
When cert-manager is enabled, cert-manager creates the Secret.
When disabled, the chart creates a self-signed Secret.
*/}}
{{- define "eks-ami-operator.webhookTLSSecretName" -}}
{{- if .Values.webhook.certManager.enabled -}}
{{ include "eks-ami-operator.fullname" . }}-serving-cert
{{- else -}}
{{ include "eks-ami-operator.fullname" . }}-webhook-tls
{{- end -}}
{{- end -}}

{{/*
Generate or retrieve self-signed webhook TLS data.
Returns a YAML map: { ca: <b64>, crt: <b64>, key: <b64> }
Uses lookup to reuse existing certs on helm upgrade (avoids cert rotation on every upgrade).
Only used when webhook.certManager.enabled=false.
*/}}
{{- define "eks-ami-operator.webhookTLS" -}}
{{- $secretName := include "eks-ami-operator.webhookTLSSecretName" . -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace $secretName -}}
{{- if $existing -}}
ca: {{ index $existing.data "ca.crt" }}
crt: {{ index $existing.data "tls.crt" }}
key: {{ index $existing.data "tls.key" }}
{{- else -}}
{{- $caObj := genCA "eks-ami-operator-ca" 3650 -}}
{{- $svc := printf "%s-webhook-service.%s.svc" (include "eks-ami-operator.fullname" .) .Release.Namespace -}}
{{- $svcFull := printf "%s-webhook-service.%s.svc.cluster.local" (include "eks-ami-operator.fullname" .) .Release.Namespace -}}
{{- $certObj := genSignedCert (include "eks-ami-operator.fullname" .) nil (list $svc $svcFull) 3650 $caObj -}}
ca: {{ $caObj.Cert | b64enc }}
crt: {{ $certObj.Cert | b64enc }}
key: {{ $certObj.Key | b64enc }}
{{- end -}}
{{- end -}}
