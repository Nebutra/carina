//go:build !darwin && !linux

package main

// Windows remains fail-closed until the native Job Object guard passes its
// descendant-process conformance suite. Process groups are not containment.
func runtimeProcessTreeContainment() string { return "none" }
