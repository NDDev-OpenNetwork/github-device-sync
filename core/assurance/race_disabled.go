//go:build !race

package assurance

// raceDetectorEnabled is a compile-time constant set when the binary is built
// with -race. See race_enabled.go for the rationale.
const raceDetectorEnabled = false
