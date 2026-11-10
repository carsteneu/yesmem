package claimtypes

// DefaultComputeAllowlist holds the command prefixes a [COMPUTE] claim may run.
// Deliberately minimal: repository-owned modules only, opened explicitly for
// anything else. No wildcard, because a future module of a package is not
// opened implicitly.
var DefaultComputeAllowlist = []string{
	"python3 -m proofboy.turing",
	"python3 -m proofboy.experiments.erdos_straus",
	"python3 -m proofboy.experiments.antihydra_deep",
}

// DefaultLintAllowlist holds the lint commands a report may ask for. It is
// separate from the test allowlist on purpose: the two claim kinds each open
// exactly the commands they mean.
var DefaultLintAllowlist = []string{
	"php -l",
	"bin/console lint:twig",
	"bin/console lint:yaml",
	"bin/console lint:container",
	"php bin/console lint:twig",
	"php bin/console lint:yaml",
	"php bin/console lint:container",
	"composer validate",
}
