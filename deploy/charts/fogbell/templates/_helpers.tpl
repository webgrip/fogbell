{{- define "fogbell.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "fogbell.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "fogbell.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fogbell.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "fogbell.labels" -}}
{{ include "fogbell.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "fogbell.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- if .Values.image.digest -}}
{{- printf "%s:%s@%s" .Values.image.repository $tag .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{- define "fogbell.config" -}}
{{- $files := dict "slack" (dict "tokenFile" "/var/run/fogbell/slack/bot-token" "signingSecretFile" "/var/run/fogbell/slack/signing-secret") -}}
{{- if .Values.roster -}}
{{- $_ := set $files "onCall" (dict "rosterFile" "/etc/fogbell/roster.yaml") -}}
{{- end -}}
{{- if .Values.tickets.clickupSecret -}}
{{- $_ := set $files "tickets" (dict "clickup" (dict "tokenFile" "/var/run/fogbell/clickup/clickup-token")) -}}
{{- end -}}
{{- if .Values.investigate.ploegSecret -}}
{{- $_ := set $files "investigate" (dict "ploeg" (dict "tokenFile" "/var/run/fogbell/ploeg/ploeg-token")) -}}
{{- end -}}
{{- mustMergeOverwrite (deepCopy .Values.config) $files | toYaml -}}
{{- end -}}
