{{- define "deploy-platform.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "deploy-platform.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "deploy-platform.labels" -}}
app.kubernetes.io/name: {{ include "deploy-platform.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "deploy-platform.selectorLabels" -}}
app.kubernetes.io/name: {{ include "deploy-platform.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "deploy-platform.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "deploy-platform.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "deploy-platform.credentialPreflightName" -}}
{{- printf "%s-credential-check" (include "deploy-platform.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
