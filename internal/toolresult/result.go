// Package toolresult defines a standard shape for tool-execution results
// across the Helixon platform. ToolResult carries status, output, error,
// latency, cost, idempotency key, retry count, and a content hash —
// enough context for cost attribution, retries, replay, and audit.
package toolresult

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/nfsarch33/helixon-platform/internal/tenantid"
)

// Status is the outcome of a tool call.
type Status string

const (
	StatusOK    Status = "ok"
	StatusError Status = "error"
)

// ToolResult is the canonical shape returned by every tool executor.
type ToolResult struct {
	Status         Status  `json:"status"`
	Output         string  `json:"output,omitempty"`
	Error          string  `json:"error,omitempty"`
	LatencyMs      int64   `json:"latency_ms"`
	CostUSD        float64 `json:"cost_usd"`
	IdempotencyKey string  `json:"idempotency_key"`
	RetryCount     int     `json:"retry_count"`
	Hash           string  `json:"hash,omitempty"`
	// TenantID attributes the cost + audit event to a tenant. v18675-3 (CF-172)
	// propagates tenant id from the per-job context (preferred) or the
	// boot-time env var. When empty, downstream audit code treats the result
	// as belonging to the "default" tenant.
	TenantID string `json:"tenant_id,omitempty"`
}

// NewToolResult builds a ToolResult with a deterministic idempotency key
// (sha256 of toolName + argsJSON, truncated to 16 hex chars) and content
// hash. Idempotency keys enable safe replay; hashes enable diff-based audit.
func NewToolResult(toolName, argsJSON string, status Status, output, errStr string, latencyMs int64, costUSD float64) ToolResult {
	combined := toolName + "|" + argsJSON
	idemHash := sha256.Sum256([]byte(combined))
	content := string(status) + "|" + output + "|" + errStr
	contentHash := sha256.Sum256([]byte(content))
	return ToolResult{
		Status:         status,
		Output:         output,
		Error:          errStr,
		LatencyMs:      latencyMs,
		CostUSD:        costUSD,
		IdempotencyKey: hex.EncodeToString(idemHash[:8]),
		Hash:           hex.EncodeToString(contentHash[:8]),
	}
}

// NewToolResultWithTenant is the v18675-3 (CF-172) entry point for
// building a ToolResult with explicit tenant attribution. Callers SHOULD
// prefer this helper when a tenant id is available from the request
// context; downstream audit/cost code reads `TenantID` to attribute the
// result. When tenantID is empty, the result is treated as belonging to
// the "default" tenant (pre-multitenancy behaviour).
func NewToolResultWithTenant(toolName, argsJSON string, status Status, output, errStr string, latencyMs int64, costUSD float64, tenantID string) ToolResult {
	r := NewToolResult(toolName, argsJSON, status, output, errStr, latencyMs, costUSD)
	r.TenantID = tenantID
	return r
}

// ResolveTenantID returns the tenant id to attach to a ToolResult,
// preferring the per-request context over the Email.TenantID-style
// fallback. Centralised here so every tool executor uses the same
// resolution rule.
func ResolveTenantID(ctx context.Context, fallback string) string {
	if v := tenantid.TenantIDFrom(ctx); v != tenantid.DefaultTenantID {
		return v
	}
	if fallback != "" {
		return fallback
	}
	return tenantid.EnvTenantID()
}

// Validate returns an error if the ToolResult is missing required fields.
// CostUSD=0 is acceptable (free local tool); missing IdempotencyKey or
// invalid Status is rejected.
func (r ToolResult) Validate() error {
	if r.Status != StatusOK && r.Status != StatusError {
		return fmt.Errorf("toolresult: invalid Status %q (want %q or %q)", r.Status, StatusOK, StatusError)
	}
	if r.IdempotencyKey == "" {
		return errors.New("toolresult: IdempotencyKey is required (set via NewToolResult)")
	}
	if r.Status == StatusError && r.Error == "" {
		return errors.New("toolresult: StatusError requires Error field to be populated")
	}
	return nil
}

// WrapErr returns a Go error from the ToolResult (Error field if StatusError,
// nil otherwise). Useful for callers that still want to surface errors via
// the standard (T, error) return signature while preserving full ToolResult.
func (r ToolResult) WrapErr() error {
	if r.Status == StatusError && r.Error != "" {
		return errors.New(r.Error)
	}
	return nil
}
