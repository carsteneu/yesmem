package proxy

import "testing"

func TestEnforceEffortFloor_NoOutputConfig(t *testing.T) {
	req := map[string]any{
		"model":   "claude-opus-4-6",
		"messages": []any{},
	}
	got := EnforceEffortFloor(req, "high")
	if !got {
		t.Fatal("expected true when output_config is absent")
	}
	oc, ok := req["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config to be created")
	}
	if oc["effort"] != "high" {
		t.Fatalf("expected effort=high, got %v", oc["effort"])
	}
}

func TestEnforceEffortFloor_UpgradesLowToHigh(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{"effort": "low"},
	}
	got := EnforceEffortFloor(req, "high")
	if !got {
		t.Fatal("expected true when upgrading low → high")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Fatalf("expected effort=high, got %v", oc["effort"])
	}
}

func TestEnforceEffortFloor_UpgradesMediumToHigh(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{"effort": "medium"},
	}
	got := EnforceEffortFloor(req, "high")
	if !got {
		t.Fatal("expected true when upgrading medium → high")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Fatalf("expected effort=high, got %v", oc["effort"])
	}
}

func TestEnforceEffortFloor_LeavesHighAlone(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{"effort": "high"},
	}
	got := EnforceEffortFloor(req, "high")
	if got {
		t.Fatal("expected false when effort already at floor")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Fatalf("expected effort=high, got %v", oc["effort"])
	}
}

func TestEnforceEffortFloor_LeavesMaxAlone(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{"effort": "max"},
	}
	got := EnforceEffortFloor(req, "high")
	if got {
		t.Fatal("expected false when effort above floor")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "max" {
		t.Fatalf("expected effort=max, got %v", oc["effort"])
	}
}

func TestEnforceEffortFloor_EmptyFloorIsNoop(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{"effort": "low"},
	}
	got := EnforceEffortFloor(req, "")
	if got {
		t.Fatal("expected false for empty floor")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "low" {
		t.Fatalf("expected effort=low unchanged, got %v", oc["effort"])
	}
}

func TestEnforceEffortFloor_PreservesOtherOutputConfigFields(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{
			"effort": "low",
			"format": map[string]any{"type": "text"},
		},
	}
	got := EnforceEffortFloor(req, "high")
	if !got {
		t.Fatal("expected true when upgrading")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Fatalf("expected effort=high, got %v", oc["effort"])
	}
	if oc["format"] == nil {
		t.Fatal("expected format field to be preserved")
	}
}

func TestEnforceEffortFloor_FloorMax(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{"effort": "high"},
	}
	got := EnforceEffortFloor(req, "max")
	if !got {
		t.Fatal("expected true when upgrading high → max")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "max" {
		t.Fatalf("expected effort=max, got %v", oc["effort"])
	}
}

func TestEnforceEffortFloor_UnknownEffortTreatedAsBelow(t *testing.T) {
	req := map[string]any{
		"output_config": map[string]any{"effort": "turbo"},
	}
	got := EnforceEffortFloor(req, "high")
	if !got {
		t.Fatal("expected true for unknown effort value")
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Fatalf("expected effort=high, got %v", oc["effort"])
	}
}
