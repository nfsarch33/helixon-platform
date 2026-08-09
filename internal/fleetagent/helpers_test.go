// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"io"
	"log/slog"
)

// nopLogger returns a slog.Logger that drops everything.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
