package setup

import (
	"fmt"

	"github.com/carsteneu/yesmem/internal/guard"
)

// printGuardStatus writes the rule guard's line into the setup report.
func printGuardStatus(dataDir, provider string) {
	for _, line := range guardStatusLines(dataDir, provider) {
		fmt.Println(line)
	}
}

// guardStatusLines is the report as text: the model the guard will ask and the endpoint it asks through. It
// resolves through guardmodel, the same source the guard reads, so the reported model is the used model. A
// test reads the lines instead of capturing stdout.
func guardStatusLines(dataDir, provider string) []string {
	resolution, err := guard.Resolve(dataDir)
	if err != nil {
		// The one case worth naming: a subscription login carries no API key, and nothing the guard could do
		// would change that. Saying it is better than leaving the user with a guard that never answers.
		if provider == "cli" {
			return []string{
				"  Rule guard: inactive. Your Claude subscription login is not an API key, and only an API key",
				"              lets the guard ask a model. The guard stays silent, it does not block anything.",
			}
		}
		return []string{
			"  Rule guard: inactive. No API key was found for a model the guard could ask (see config.yaml).",
		}
	}
	return []string{
		"  Rule guard:",
		fmt.Sprintf("    Model:    %s  (%s)", resolution.Model, resolution.Source),
		fmt.Sprintf("    Endpoint: %s", resolution.Host),
	}
}
