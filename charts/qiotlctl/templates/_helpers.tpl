{{- define "qiotlctl.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "qiotlctl.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains (include "qiotlctl.name" .) .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "qiotlctl.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- define "qiotlctl.selectorLabels" -}}
app.kubernetes.io/name: {{ include "qiotlctl.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "qiotlctl.labels" -}}
{{ include "qiotlctl.selectorLabels" . }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "qiotlctl.configName" -}}
{{- printf "%s-config" (include "qiotlctl.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "qiotlctl.claimName" -}}
{{- default (include "qiotlctl.fullname" .) .Values.persistence.existingClaim -}}
{{- end -}}
{{- define "qiotlctl.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "qiotlctl.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
{{- define "qiotlctl.image" -}}
{{- if .Values.image.digest -}}
{{ printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else -}}
{{ printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end -}}
{{- end -}}
{{- define "qiotlctl.vmUserName" -}}
{{- default (include "qiotlctl.fullname" .) .Values.vmUser.name -}}
{{- end -}}
{{- define "qiotlctl.validate" -}}
{{- if and .Values.config.existingSecret .Values.config.existingConfigMap -}}
{{- fail "choose config.existingSecret or config.existingConfigMap" -}}
{{- end -}}
{{- if and .Values.vmUser.enabled .Values.vmCredentials.existingSecret -}}
{{- fail "choose vmUser.enabled or vmCredentials.existingSecret" -}}
{{- end -}}
{{- if and (not .Values.persistence.enabled) .Values.persistence.existingClaim -}}
{{- fail "persistence.existingClaim requires persistence.enabled" -}}
{{- end -}}
{{- if and (not .Values.persistence.enabled) (not .Values.persistence.allowEphemeral) -}}
{{- fail "disabling persistence requires explicit persistence.allowEphemeral=true" -}}
{{- end -}}
{{- if hasKey .Values.config.files .Values.config.configFilename -}}
{{- fail "config.files must not replace config.configFilename; use an overlay" -}}
{{- end -}}
{{- range $key := list "app.kubernetes.io/name" "app.kubernetes.io/instance" -}}
{{- if hasKey $.Values.podLabels $key -}}{{- fail "podLabels must not override selector labels" -}}{{- end -}}
{{- end -}}
{{- if hasKey .Values.podAnnotations "checksum/config" -}}{{- fail "checksum/config is chart-managed" -}}{{- end -}}
{{- if hasKey .Values.persistence.annotations "helm.sh/resource-policy" -}}
{{- fail "use persistence.retain instead of overriding helm.sh/resource-policy" -}}
{{- end -}}
{{- if and .Values.vmUser.enabled (or (ne .Values.vmCredentials.usernameKey "username") (ne .Values.vmCredentials.passwordKey "password")) -}}
{{- fail "VMUser generated credentials use username/password keys" -}}
{{- end -}}
{{- $env := dict -}}
{{- if .Values.mqttCredentials.existingSecret -}}
{{- if .Values.mqttCredentials.usernameKey -}}{{- $_ := set $env "QIOTLCTL_MQTT_USERNAME" true -}}{{- end -}}
{{- if .Values.mqttCredentials.passwordKey -}}{{- $_ := set $env "QIOTLCTL_MQTT_PASSWORD" true -}}{{- end -}}
{{- end -}}
{{- if or .Values.vmUser.enabled .Values.vmCredentials.existingSecret -}}
{{- if .Values.vmCredentials.usernameKey -}}{{- $_ := set $env "QIOTLCTL_VM_USERNAME" true -}}{{- end -}}
{{- if .Values.vmCredentials.passwordKey -}}{{- $_ := set $env "QIOTLCTL_VM_PASSWORD" true -}}{{- end -}}
{{- end -}}
{{- range .Values.extraEnv -}}
{{- if hasKey $env .name -}}{{- fail (printf "duplicate environment variable: %s" .name) -}}{{- end -}}
{{- $_ := set $env .name true -}}
{{- end -}}
{{- end -}}

{{/* Normalize only the initial document marker; preserve raw overlay values. */}}
{{- define "qiotlctl.yamlDocument" -}}
{{- if regexMatch "^---([ \t\r\n]|$)" . -}}
{{- . -}}
{{- else -}}
{{- printf "---\n%s" . -}}
{{- end -}}
{{- end -}}
