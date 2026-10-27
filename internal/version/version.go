// Package version holds build metadata that is stamped into each binary at
// compile time with -ldflags, for example:
//
//	go build -ldflags "-X flaketracker/internal/version.Commit=$(git rev-parse --short HEAD) -X flaketracker/internal/version.Algorithm=v2" ./cmd/analyzer
//
// -X overwrites the initial value of a package-level string variable when the
// binary is linked. That's what makes the rollout demo's v2 a genuinely
// different container image rather than a config switch: which scoring
// algorithm the analyzer runs is baked in when the image is built
// (Section 10.4 of the project plan).
package version

// These must be `var` (not `const`) because -X can only overwrite variables.
var (
	Version   = "dev"     // release label, e.g. "1.2.0"
	Commit    = "unknown" // git commit SHA the binary was built from
	Algorithm = "v1"      // analyzer scoring algorithm: "v1" or "v2"
)
