package setup

func deepMergeJSON(target, source map[string]any) {
	if source == nil || target == nil {
		return
	}
	for key, srcVal := range source {
		tgtVal, tgtExists := target[key]
		if !tgtExists {
			target[key] = deepCopyValue(srcVal)
			continue
		}
		srcMap, srcIsMap := srcVal.(map[string]any)
		tgtMap, tgtIsMap := tgtVal.(map[string]any)
		if srcIsMap && tgtIsMap {
			deepMergeJSON(tgtMap, srcMap)
		}
	}
}

func deepCopyValue(v any) any {
	if m, ok := v.(map[string]any); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = deepCopyValue(val)
		}
		return out
	}
	if arr, ok := v.([]any); ok {
		out := make([]any, len(arr))
		for i, val := range arr {
			out[i] = deepCopyValue(val)
		}
		return out
	}
	return v
}
