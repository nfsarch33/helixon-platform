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

	lines := strings.Split(string(raw), "\n")
	var currentKey *vendorKey
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		// list-item lines under `keys:`
		if strings.HasPrefix(trim, "- alias:") {
			if currentKey != nil {
				cfg.keys = append(cfg.keys, *currentKey)
			}
			currentKey = &vendorKey{}
			currentKey.Alias = strings.TrimSpace(strings.TrimPrefix(trim, "- alias:"))
			continue
		}
		// nested key under currentKey
		if currentKey != nil && len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			k, v := splitKV(strings.TrimSpace(line))
			switch k {
			case "vault":
				currentKey.Vault = v
			case "item_id":
				currentKey.ItemID = v
			case "field":
				currentKey.Field = v
			case "vendor":
				currentKey.Vendor = v
			case "status":
				currentKey.Status = v
			case "notes":
				currentKey.Notes = strings.Trim(v, "\"")
			case "last_used_at":
				currentKey.LastUsedAt = v
			case "daily_quota":
				// not used at runtime; kept for documentation
			}
			continue
		}
		// top-level keys
		if currentKey != nil {
			cfg.keys = append(cfg.keys, *currentKey)
			currentKey = nil
		}
		k, v := splitKV(trim)
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
			if strings.HasPrefix(trim, "resend:") || strings.HasPrefix(trim, "brevo:") || strings.HasPrefix(trim, "smtp2go:") {
				k2, v2 := splitKV(trim)
				cfg.from[k2] = v2
			}
		}
	}
	if currentKey != nil {
		cfg.keys = append(cfg.keys, *currentKey)
	}
	if cfg.recipients.primary == "" {
		cfg.recipients.primary = "jaslian@gmail.com"
	}
	if cfg.rotateAfter == 0 {
		cfg.rotateAfter = 60 * time.Second
	}
	return cfg, nil
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
