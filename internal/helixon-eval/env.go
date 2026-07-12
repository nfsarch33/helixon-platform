// env_unix.go -- stub-friendly os.Getenv wrapper for live_source tests.
//
// Build tag is deliberately empty so this file compiles on every
// platform supported by the runner. Tests in live_source_test.go swap
// lookupEnv for a stub map.
package helixoneval

import "os"

func syscallGetenv(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	return v, ok
}
