// Package svcregistry is the v3 service registry for the Helixon platform.
//
// It tracks user-level services (name, host, port, protocol, owner, status,
// last_seen_iso, tailscale_ip), persists the snapshot to
// ~/.config/svc-registry.json, exposes Prometheus metrics
// svcregistry_operations_total{op=...,status=...}, and detects port
// collisions across hosts and protocols.
//
// The package is intentionally file-backed (not SQLite) so the snapshot is
// human-readable and diffable in git history. All writes go through an
// atomic rename so a crash mid-write never leaves a half-truncated file.
package svcregistry
