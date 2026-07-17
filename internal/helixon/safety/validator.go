package safety

import (
	"regexp"
	"strings"
)

// InjectionPattern is a compiled regex with a category label.
type InjectionPattern struct {
	Pattern  *regexp.Regexp
	Category string
}

// ValidationResult captures the outcome of input validation.
type ValidationResult struct {
	Safe            bool     `json:"safe"`
	TenantID        string   `json:"tenant_id,omitempty"` // v18686-1: multi-tenancy
	MatchedPatterns []string `json:"matched_patterns,omitempty"`
	Categories      []string `json:"categories,omitempty"`
}

var defaultPatterns = []InjectionPattern{
	{regexp.MustCompile(`(?i)ignore\s+(all\s+)?(previous|above|prior)\s+(instructions|prompts?|rules?)`), "OVERRIDE_ATTEMPT"},
	{regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)\s+`), "ROLE_HIJACK"},
	{regexp.MustCompile(`(?i)system\s*:\s*`), "SYSTEM_INJECTION"},
	{regexp.MustCompile(`(?i)forget\s+(everything|all|your\s+(instructions|rules|training))`), "MEMORY_WIPE"},
	{regexp.MustCompile(`(?i)\[INST\]|\[/INST\]|<<SYS>>|<\|im_start\|>`), "DELIMITER_INJECTION"},
	{regexp.MustCompile(`(?i)pretend\s+(to\s+be|you\s+are|that)`), "PERSONA_OVERRIDE"},
	{regexp.MustCompile(`(?i)do\s+not\s+follow\s+(any|your|the)\s+(safety|content|moderation)`), "SAFETY_BYPASS"},
	{regexp.MustCompile(`(?i)(execute|run|eval)\s*\(`), "CODE_INJECTION"},
	{regexp.MustCompile(`(?i)__(import|builtins|globals)__`), "PYTHON_INJECTION"},
	{regexp.MustCompile(`(?i);\s*(rm|del|drop|shutdown|kill)\s+`), "COMMAND_INJECTION"},
}

// InputValidator checks user input for prompt injection and other safety concerns.
type InputValidator struct {
	patterns []InjectionPattern
}

// NewInputValidator creates a validator with the default pattern set.
func NewInputValidator() *InputValidator {
	return &InputValidator{patterns: defaultPatterns}
}

// NewInputValidatorWithPatterns creates a validator with custom patterns.
func NewInputValidatorWithPatterns(patterns []InjectionPattern) *InputValidator {
	return &InputValidator{patterns: patterns}
}

// AddPattern appends a custom pattern to the validator.
func (v *InputValidator) AddPattern(pattern *regexp.Regexp, category string) {
	v.patterns = append(v.patterns, InjectionPattern{Pattern: pattern, Category: category})
}

// Validate checks input text against all patterns.
func (v *InputValidator) Validate(input string) ValidationResult {
	result := ValidationResult{Safe: true}

	for _, p := range v.patterns {
		if p.Pattern.MatchString(input) {
			result.Safe = false
			result.MatchedPatterns = append(result.MatchedPatterns, p.Pattern.String())
			result.Categories = append(result.Categories, p.Category)
		}
	}

	return result
}

// ValidateMultiple checks multiple text fields (message content, tool args, etc.).
func (v *InputValidator) ValidateMultiple(texts []string) ValidationResult {
	merged := ValidationResult{Safe: true}
	for _, text := range texts {
		r := v.Validate(text)
		if !r.Safe {
			merged.Safe = false
			merged.MatchedPatterns = append(merged.MatchedPatterns, r.MatchedPatterns...)
			merged.Categories = append(merged.Categories, r.Categories...)
		}
	}
	return merged
}

// QuickCheck returns true if the input appears safe.
func (v *InputValidator) QuickCheck(input string) bool {
	return v.Validate(input).Safe
}

// PatternCount returns the number of loaded patterns.
func (v *InputValidator) PatternCount() int {
	return len(v.patterns)
}

// ContainsSensitiveData performs a basic check for common sensitive data patterns.
func ContainsSensitiveData(text string) []string {
	var findings []string
	lower := strings.ToLower(text)

	if matched, _ := regexp.MatchString(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`, text); matched {
		findings = append(findings, "email_address")
	}
	if strings.Contains(lower, "sk-") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
		findings = append(findings, "api_key")
	}
	if matched, _ := regexp.MatchString(`\b\d{3}[-.]?\d{3}[-.]?\d{4}\b`, text); matched {
		findings = append(findings, "phone_number")
	}

	return findings
}
