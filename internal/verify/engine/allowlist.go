package engine

// DefaultCommandAllowlist is the set of test-runner prefixes a report may ask
// for. A report is a claim, not a trusted script, so anything outside this
// list stays unverifiable.
//
// Wrapper entries (make, npm, yarn, bun, pnpm, npx, composer) must show the
// inner runner in their output; the runner checker refuses a wrapper output
// without test evidence, so a wrapper cannot launder a green claim.
var DefaultCommandAllowlist = []string{
	"python3 -m unittest",
	"python3 -m pytest",
	"python -m unittest",
	"python -m pytest",
	"pytest",
	"go test",
	"cargo test",
	"npm test",
	"npm run test",
	"yarn test",
	"bun test",
	"node --test",
	"make test",
	"make check",
	"phpunit",
	"bin/phpunit",
	"vendor/bin/phpunit",
	"composer test",
	"composer run test",
}
