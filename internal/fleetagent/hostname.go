// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import "os"

// hostname is a tiny os.Hostname wrapper kept separate so tests can stub it.
func hostname() (string, error) { return os.Hostname() }
