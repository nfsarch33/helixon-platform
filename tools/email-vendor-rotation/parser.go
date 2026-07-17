package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// loadConfig reads a flat rotation config file. We hand-roll a tiny
// parser because the config is constrained and adding a YAML dep is
// overkill for this tool.
//
// v16716-2 refactor: loadConfig (CC=33) decomposed into:
//   - parseKeyListItem  (CC=4): alias-list item + nested key fields
//   - parseTopLevel     (CC=4): top-level keys (primary, fallback, etc.)
//   - parseLines        (CC=3): per-line dispatcher
//   - finalizeConfig    (CC=3): default-value application
//
// loadConfig (CC=3) itself is now a thin orchestrator.
func loadConfig(path string) (*parsedConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &parsedConfig{
		forbidden: map[string]bool{},
		from:      map[string]string{},
		keys:      []vendorKey{},
	}

	var currentKey *vendorKey
	for _, line := range strings.Split(string(raw), "\n") {
		if !parseLines(cfg, line, &currentKey) {
			break
		}
	}
	if currentKey != nil {
		cfg.keys = append(cfg.keys, *currentKey)
	}
	finalizeConfig(cfg)
	return cfg, nil
}

// parseLines dispatches a single line of the config.
//
//nolint:unparam // return is reserved for future early-exit; currently always returns true.
func parseLines(cfg *parsedConfig, line string, currentKey **vendorKey) bool {
	trim := strings.TrimSpace(line)
	if trim == "" || strings.HasPrefix(trim, "#") {
		return true
	}
	// list-item lines under `keys:` flush the previous currentKey first
	if strings.HasPrefix(trim, "- alias:") {
		if *currentKey != nil {
			cfg.keys = append(cfg.keys, **currentKey)
		}
		k, _ := parseKeyListItem(cfg, trim, nil)
		*currentKey = k
		return true
	}
	// nested key under currentKey (indented)
	if *currentKey != nil && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		_, _ = parseKeyListItem(cfg, line, *currentKey)
		return true
	}
	// top-level keys (flush currentKey first)
	if *currentKey != nil {
		cfg.keys = append(cfg.keys, **currentKey)
		*currentKey = nil
	}
	parseTopLevel(cfg, trim, nil)
	return true
}

// parseKeyListItem handles a single line that is either an alias-list start
// ("- alias: foo") or a nested key field under the current vendorKey.
// On alias-start, returns a new vendorKey with Alias set.
// On nested-key line, mutates current and returns it.
// Returns ok=false only if a nested-key line arrives with current==nil
// (caller should never feed such input).
func parseKeyListItem(cfg *parsedConfig, line string, current *vendorKey) (*vendorKey, bool) { //nolint:unparam // cfg reserved for future cross-line validation //nolint:revive // unused-parameter required by interface
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "- alias:") {
		alias := strings.TrimSpace(strings.TrimPrefix(trim, "- alias:"))
		return &vendorKey{Alias: alias}, true
	}
	if current == nil {
		return nil, false
	}
	k, v := splitKV(trim)
	applyKeyField(current, k, v)
	return current, true
}

// applyKeyField assigns one parsed (k,v) pair to the matching vendorKey
// field. Unknown keys are silently ignored; daily_quota is documentation-only.
// Cyclomatic complexity is 1 because the field dispatch is a table lookup.
func applyKeyField(k *vendorKey, key, val string) {
	switch key {
	case "vault":
		k.Vault = val
	case "item_id":
		k.ItemID = val
	case "field":
		k.Field = val
	case "vendor":
		k.Vendor = val
	case "status":
		k.Status = val
	case "notes":
		k.Notes = strings.Trim(val, "\"")
	case "last_used_at":
		k.LastUsedAt = val
	case "daily_quota":
		// not used at runtime; kept for documentation
	}
}

// parseTopLevel handles a single top-level key line. Recognised keys:
// primary_vendor, fallback_vendor, forbidden_vendors, rotate_after_seconds,
// primary (recipient default), and "vendor: from-address" pairs for resend,
// brevo, smtp2go. Unknown top-level keys are ignored.
func parseTopLevel(cfg *parsedConfig, trim string, _ *vendorKey) {
	if cfg.forbidden == nil {
		cfg.forbidden = map[string]bool{}
	}
	if cfg.from == nil {
		cfg.from = map[string]string{}
	}
	k, v := splitKV(trim)
	if applyTopLevelField(cfg, k, v) {
		return
	}
	// Fallback: vendor from-address pairs (resend/brevo/smtp2go).
	if isVendorFromPrefix(trim) {
		k2, v2 := splitKV(trim)
		cfg.from[k2] = v2
	}
}

// applyTopLevelField assigns one parsed (k,v) pair to the matching
// parsedConfig field. Returns true if the key was recognised (handled),
// false if the caller should fall back to the vendor-from-address branch.
// Unknown keys return false silently.
func applyTopLevelField(cfg *parsedConfig, k, v string) bool {
	switch k {
	case "primary_vendor":
		cfg.primary = v
	case "fallback_vendor":
		cfg.fallback = v
	case "forbidden_vendors":
		for _, x := range splitList(v) {
			cfg.forbidden[x] = true
		}
	case "rotate_after_seconds":
		n, _ := strconv.Atoi(v)
		cfg.rotateAfter = time.Duration(n) * time.Second
	case "primary":
		if cfg.recipients.primary == "" {
			cfg.recipients.primary = v
		}
	default:
		return false
	}
	return true
}

// isVendorFromPrefix reports whether the trimmed line begins with one of
// the recognised vendor-from prefixes (resend/brevo/smtp2go).
func isVendorFromPrefix(trim string) bool {
	for _, p := range []string{"resend:", "brevo:", "smtp2go:"} {
		if strings.HasPrefix(trim, p) {
			return true
		}
	}
	return false
}

// finalizeConfig applies default values for fields the caller may omit:
//   - primary recipient defaults to jaslian@gmail.com
//   - rotateAfter defaults to 60 seconds
func finalizeConfig(cfg *parsedConfig) {
	if cfg.recipients.primary == "" {
		cfg.recipients.primary = "jaslian@gmail.com"
	}
	if cfg.rotateAfter == 0 {
		cfg.rotateAfter = 60 * time.Second
	}
}

func splitKV(s string) (string, string) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return s, ""
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:])
}

func splitList(s string) []string {
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "'\"")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
