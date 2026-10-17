//go:build race

package datalog

// raceDetector says the tests were built with -race, under which the generated corpus skips (#143).
const raceDetector = true
