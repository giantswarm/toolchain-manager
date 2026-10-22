{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label. A label value is at
most 63 characters and begins and ends alphanumeric: the cut of a long version
(a branch build's <version>-dev.<branch>.<date>.<time>.<sha>, or the
<version>+<digest> helm-controller installs) can land on any run of ".", "_"
(from "+") and "-", so the whole run is trimmed.
*/}}
{{- define "chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimAll "-._" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "labels.common" -}}
app: {{ include "name" . | quote }}
{{ include "labels.selector" . }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
application.giantswarm.io/team: {{ index .Chart.Annotations "io.giantswarm.application.team" | quote }}
helm.sh/chart: {{ include "chart" . | quote }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "labels.selector" -}}
app.kubernetes.io/name: {{ include "name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{/*
"true" when every MCP call is authenticated by the person's Dex ID token and
the source tools are served.
*/}}
{{- define "toolchain-manager.auth" -}}
{{- if .Values.auth.dex.issuerURL }}true{{ else }}false{{ end -}}
{{- end -}}

{{/*
The trusted audiences: auth.trustedAudiences and the ones muster requests,
which every forwarded token carries.
*/}}
{{- define "toolchain-manager.audiences" -}}
{{- $a := concat (.Values.auth.trustedAudiences | default list) (.Values.muster.mcpServer.requiredAudiences | default list) | uniq -}}
{{- if not $a }}{{ fail "auth.trustedAudiences names no audience: the Dex client ids whose ID tokens muster forwards" }}{{ end -}}
{{- join "," $a -}}
{{- end -}}
