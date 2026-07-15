package main

// appVersion is the version shown inside Parley. Release builds override this
// value with the exact GitHub release version via -ldflags -X, while local builds
// use the current checked-in release as their baseline.
var appVersion = "0.1.1"
