package safety

import (
	"regexp"
	"strings"
)

var (
	shellCommandRe   = regexp.MustCompile("(?m)^\\s*[$#>]\\s*(rm|del|kill|shutdown|reboot|sudo|chmod|chown|mkfs|dd\\s|format)\\s")
	envVarLeakRe     = regexp.MustCompile(`(?i)(OPENAI_API_KEY|ANTHROPIC_API_KEY|AWS_SECRET|GITHUB_TOKEN|DATABASE_URL|DB_PASSWORD)\s*[=:]\s*\S+`)
	filePathLeakRe   = regexp.MustCompile(`(/etc/passwd|/etc/shadow|\.env|\.ssh/id_rsa|credentials\.json)`)
	privateIPRe      = regexp.MustCompile(`\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[0-1])\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3})\b`)
	connectionStrRe  = regexp.MustCompile(`(?i)(postgres|mysql|mongodb|redis)://[^\s]+`)
)

// SanitizeResult describes what was cleaned from the output.
type SanitizeResult struct {
	Output          string   `json:"output"`
	RedactedCount   int      `json:"redacted_count"`
	RedactedTypes   []string `json:"redacted_types,omitempty"`
	ShellLeakFound  bool     `json:"shell_leak_found"`
}

// OutputSanitizer removes dangerous or sensitive content from agent output.
type OutputSanitizer struct {
	redactShellCmds   bool
	redactEnvVars     bool
	redactFilePaths   bool
	redactPrivateIPs  bool
	redactConnStrings bool
}

// SanitizerOption configures the sanitizer.
type SanitizerOption func(*OutputSanitizer)

// WithRedactShellCmds enables/disables shell command redaction.
func WithRedactShellCmds(v bool) SanitizerOption {
	return func(s *OutputSanitizer) { s.redactShellCmds = v }
}

// WithRedactAll enables all redaction categories.
func WithRedactAll() SanitizerOption {
	return func(s *OutputSanitizer) {
		s.redactShellCmds = true
		s.redactEnvVars = true
		s.redactFilePaths = true
		s.redactPrivateIPs = true
		s.redactConnStrings = true
	}
}

// NewOutputSanitizer creates a sanitizer with the given options.
// By default all categories are enabled.
func NewOutputSanitizer(opts ...SanitizerOption) *OutputSanitizer {
	s := &OutputSanitizer{
		redactShellCmds:   true,
		redactEnvVars:     true,
		redactFilePaths:   true,
		redactPrivateIPs:  true,
		redactConnStrings: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Sanitize cleans the output text, redacting sensitive patterns.
func (s *OutputSanitizer) Sanitize(output string) SanitizeResult {
	result := SanitizeResult{Output: output}

	if s.redactShellCmds {
		if shellCommandRe.MatchString(output) {
			result.ShellLeakFound = true
			result.Output = shellCommandRe.ReplaceAllString(result.Output, "[REDACTED: shell command]")
			result.RedactedCount++
			result.RedactedTypes = append(result.RedactedTypes, "shell_command")
		}
	}

	if s.redactEnvVars {
		matches := envVarLeakRe.FindAllString(result.Output, -1)
		if len(matches) > 0 {
			result.Output = envVarLeakRe.ReplaceAllString(result.Output, "[REDACTED: env variable]")
			result.RedactedCount += len(matches)
			result.RedactedTypes = append(result.RedactedTypes, "env_variable")
		}
	}

	if s.redactFilePaths {
		matches := filePathLeakRe.FindAllString(result.Output, -1)
		if len(matches) > 0 {
			result.Output = filePathLeakRe.ReplaceAllString(result.Output, "[REDACTED: sensitive path]")
			result.RedactedCount += len(matches)
			result.RedactedTypes = append(result.RedactedTypes, "file_path")
		}
	}

	if s.redactPrivateIPs {
		matches := privateIPRe.FindAllString(result.Output, -1)
		if len(matches) > 0 {
			result.Output = privateIPRe.ReplaceAllString(result.Output, "[REDACTED: private IP]")
			result.RedactedCount += len(matches)
			result.RedactedTypes = append(result.RedactedTypes, "private_ip")
		}
	}

	if s.redactConnStrings {
		matches := connectionStrRe.FindAllString(result.Output, -1)
		if len(matches) > 0 {
			result.Output = connectionStrRe.ReplaceAllString(result.Output, "[REDACTED: connection string]")
			result.RedactedCount += len(matches)
			result.RedactedTypes = append(result.RedactedTypes, "connection_string")
		}
	}

	return result
}

// StripMarkdownCodeFences removes ``` fenced code blocks that might contain
// executable content. Used for safety-critical output channels.
func StripMarkdownCodeFences(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
