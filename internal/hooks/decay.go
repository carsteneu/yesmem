package hooks

const (
	decayMinInjections = 20
	decayPrecisionCap  = 0.1
	decayFloor         = 0.3
)

func injectionDecay(injectCount, useCount, saveCount int) float64 {
	if injectCount < decayMinInjections || injectCount <= 0 {
		return 1.0
	}
	effectiveUses := float64(max(useCount, 0)) + float64(max(saveCount, 0))*2
	ratio := effectiveUses / float64(injectCount)
	if ratio >= decayPrecisionCap {
		return 1.0
	}
	return decayFloor + (1.0-decayFloor)/decayPrecisionCap*ratio
}
