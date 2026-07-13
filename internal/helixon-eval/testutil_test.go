// testutil_test.go — shared test helpers across registry_test.go and
// helpers_test.go. (approxEqual lives in report_test.go because Go's
// test build links by package, not by file.)
package helixoneval

import "math"

// floatFromInt returns a stable float for known integer inputs.
func floatFromInt(i int) float64 { return math.Round(float64(i)*1000) / 1000 }
