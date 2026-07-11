//go:build darwin || linux

package main

func runtimeProcessTreeContainment() string { return "unix_pgrp_v1" }
