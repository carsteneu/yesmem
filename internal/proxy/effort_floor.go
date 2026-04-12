package proxy

// effortRank maps API effort values to ordinal rank for comparison.
var effortRank = map[string]int{
	"low":    0,
	"medium": 1,
	"high":   2,
	"max":    3,
}

// EnforceEffortFloor ensures the request's output_config.effort is at least
// floor. If absent or below floor, it is set to floor. Returns true if the
// request was modified. No-op when floor is empty.
func EnforceEffortFloor(req map[string]any, floor string) bool {
	if floor == "" {
		return false
	}
	floorRank, floorKnown := effortRank[floor]
	if !floorKnown {
		return false
	}

	oc, _ := req["output_config"].(map[string]any)
	if oc == nil {
		req["output_config"] = map[string]any{"effort": floor}
		return true
	}

	cur, _ := oc["effort"].(string)
	curRank, known := effortRank[cur]
	if !known || curRank < floorRank {
		oc["effort"] = floor
		return true
	}
	return false
}
