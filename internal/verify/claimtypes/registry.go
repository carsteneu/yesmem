// Package claimtypes holds one file per claim kind. The registry below is the
// only list: report parsing and judgement dispatch both read it, so a new kind
// never touches either.
package claimtypes

import "github.com/carsteneu/yesmem/internal/verify/engine"

// All returns the registered claim types.
//
// The core marker kinds (commit, branch, merge, deploy) and the line kinds
// (tests_green, tests_exit, diff_scope, unknown_marker, profile) bring no
// Match: their claims come from the core loops of the parser, and this registry
// supplies the checkers and the marker tokens. A kind with a Match brings its
// own marker pattern.
func All() []engine.ClaimType {
	return []engine.ClaimType{
		{Kind: "commit_exists", Markers: []string{"COMMIT"}, Check: CheckCommit, NeedsRepo: true},
		{Kind: "branch_pushed", Markers: []string{"BRANCH"}, Check: CheckBranchPushed, NeedsRepo: true, BindsCommit: true},
		{Kind: "merge", Markers: []string{"MERGE"}, Check: CheckMerge, BindsCommit: true},
		{Kind: "deploy", Markers: []string{"DEPLOY"}, Check: CheckDeploy},
		{Kind: "unknown_marker", Check: CheckUnknownMarker},
		{Kind: "profile", Check: CheckProfile},
		{Kind: "worktree_clean", Check: CheckWorktreeClean, NeedsRepo: true},
		{Kind: "diff_scope", Check: CheckDiffScope, NeedsRepo: true},
		{Kind: "tests_green", Check: CheckTests, NeedsRepo: true},
		{Kind: "tests_exit", Check: CheckTests, NeedsRepo: true},
		{Kind: "compute", Markers: []string{"COMPUTE"}, Match: MatchCompute, Check: CheckCompute, NeedsRepo: true, BindsCommit: true},
		{Kind: "artifact", Markers: []string{"ARTIFACT"}, Match: MatchArtifact, Check: CheckArtifact},
		{Kind: "lint", Markers: []string{"LINT"}, Match: MatchLint, Check: CheckLint, NeedsRepo: true, BindsCommit: true},
		{Kind: "evidence_ids", Markers: []string{"EVIDENCE"}, Match: MatchEvidence, Check: CheckEvidence},
		{Kind: "plan_items", Markers: []string{"PLAN"}, Match: MatchPlan, Check: CheckPlanItems},
		{Kind: "mutation", Markers: []string{"MUTATION"}, Match: MatchMutation, Check: CheckMutation, NeedsRepo: true, BindsCommit: true},
		{Kind: "coverage_delta", Markers: []string{"COVERAGE"}, Match: MatchCoverageDelta, Check: CheckCoverageDelta, NeedsRepo: true, BindsCommit: true},
	}
}

// MarkerTokens returns every marker token a registered type claims.
func MarkerTokens() map[string]bool {
	tokens := make(map[string]bool)
	for _, claimType := range All() {
		for _, marker := range claimType.Markers {
			tokens[marker] = true
		}
	}
	return tokens
}
