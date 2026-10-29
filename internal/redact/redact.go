// Package redact replaces obvious secrets in a log line before fogbell posts it.
//
// It is not a guarantee: a pod that logs a credential in a shape nobody listed still leaks it, which is
// why fogbell posts only a few lines of one pod.
package redact

import "regexp"

var rules = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)\b((?:proxy-)?authorization)(\s*[:=]\s*)(?:(?:bearer|basic|token)\s+)?[^\s,;]+`), "${1}${2}[redacted]"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`), "Bearer [redacted]"},
	{regexp.MustCompile(`(?i)\b([\w-]*(?:token|secret|passw(?:or)?d|api[_-]?key|access[_-]?key|credentials?))(\\?["']?\s*[:=]\s*\\?["']?)[^\s"',;&}]+`), "${1}${2}[redacted]"},
	{regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@`), "://[redacted]@"},
	{regexp.MustCompile(`\b(?:glpat|glrt|gldt|glptt)-[\w-]{10,}`), "[redacted]"},
	{regexp.MustCompile(`\bxox[abposr]-[\w-]{10,}`), "[redacted]"},
	{regexp.MustCompile(`\bgh[pousr]_\w{20,}`), "[redacted]"},
	{regexp.MustCompile(`\bsk-[\w-]{16,}`), "[redacted]"},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[redacted]"},
	{regexp.MustCompile(`\beyJ[\w-]{8,}\.[\w-]{8,}\.[\w-]{8,}`), "[redacted-jwt]"},
}

// Line replaces the secrets it recognises in a log line.
func Line(line string) string {
	for _, r := range rules {
		line = r.re.ReplaceAllString(line, r.repl)
	}
	return line
}
