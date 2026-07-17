package svcregistry

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Service is a single registered entry. Field order matches the schema
// agreed in the v16122-1 brief:
//
//	{name, host, port, protocol, owner, status, last_seen_iso, tailscale_ip, tenant_id}
type Service struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Protocol    string `json:"protocol"`
	Owner       string `json:"owner"`
	Status      string `json:"status"`        // "up" | "down" | "unknown"
	LastSeenISO string `json:"last_seen_iso"` // RFC3339
	TailscaleIP string `json:"tailscale_ip"`
	// TenantID (v18675-3, CF-172) attributes the service registration to a
	// tenant. Single-tenant deployments leave this empty and downstream
	// cost code treats it as "default". Multi-tenant deployments pass the
	// boot-time HELIXON_TENANT_ID env var or the per-request context value.
	TenantID string `json:"tenant_id,omitempty"`
}

// Operation names for the Prometheus counter labels.
const (
	OpRegister   = "register"
	OpUnregister = "unregister"
	OpList       = "list"
	OpConflict   = "conflict"
)

// Status values for the Prometheus counter labels.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Sentinel errors that callers must handle with errors.Is.
var (
	ErrInvalidName     = errors.New("svcregistry: invalid service name")
	ErrInvalidHost     = errors.New("svcregistry: invalid host")
	ErrInvalidPort     = errors.New("svcregistry: invalid port (must be 1..65535)")
	ErrInvalidProtocol = errors.New("svcregistry: invalid protocol (must be tcp|udp|http|grpc|https)")
	ErrInvalidStatus   = errors.New("svcregistry: invalid status (must be up|down|unknown)")
	ErrNotFound        = errors.New("svcregistry: service not found")
	ErrPortConflict    = errors.New("svcregistry: port already registered")
)

// validProtocols lists the acceptable wire-protocol labels.
var validProtocols = map[string]struct{}{
	"tcp":   {},
	"udp":   {},
	"http":  {},
	"https": {},
	"grpc":  {},
}

// validStatuses lists the acceptable service status values.
var validStatuses = map[string]struct{}{
	"up":      {},
	"down":    {},
	"unknown": {},
}

// Validate returns nil iff the service satisfies all schema constraints.
// Empty TailscaleIP and Owner are allowed; empty LastSeenISO is auto-set
// to the current UTC RFC3339 timestamp.
func (s Service) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return ErrInvalidName
	}
	if strings.TrimSpace(s.Host) == "" {
		return ErrInvalidHost
	}
	if s.Port < 1 || s.Port > 65535 {
		return ErrInvalidPort
	}
	if _, ok := validProtocols[s.Protocol]; !ok {
		return ErrInvalidProtocol
	}
	if _, ok := validStatuses[s.Status]; !ok {
		return ErrInvalidStatus
	}
	return nil
}

// Key returns the (host, name) tuple that uniquely identifies a service.
func (s Service) Key() string {
	return fmt.Sprintf("%s/%s", s.Host, s.Name)
}

// WithDefaults returns a copy of s with LastSeenISO set to now-UTC if it
// was empty. It does not mutate the receiver.
func (s Service) WithDefaults() Service {
	if s.LastSeenISO == "" {
		s.LastSeenISO = time.Now().UTC().Format(time.RFC3339)
	}
	return s
}
