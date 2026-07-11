package main

import "strings"

// stripConfigArgs removes the config flag (both "--config value" and
// "--config=value" forms, single or double dash) from args; config.Load
// owns every other flag.
func stripConfigArgs(args []string) []string {
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--config" || a == "-config" {
			i++
			continue
		}
		if strings.HasPrefix(a, "--config=") || strings.HasPrefix(a, "-config=") {
			continue
		}
		filtered = append(filtered, a)
	}
	return filtered
}
