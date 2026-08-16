//go:build race

package assurance

// raceDetectorEnabled is a compile-time constant set when the binary is built
// with -race. It is used by the acceptance policy to treat the race detector's
// constant slowdown as a timing-relaxed environment, so wall-clock budgets that
// fail only under -race do not fail the gate. The same files without _test.go
// suffix let production code (core/cmd/gds-assurance) reference this constant.
const raceDetectorEnabled = true
