package main

import (
	"os"

	"pi/internal/linkerexec"
)

// guardBinaryIdentity reports the resolved executable path.
func guardBinaryIdentity() string {
	resolved, err := linkerexec.Executable()
	if err != nil {
		return os.Args[0]
	}
	return resolved
}
