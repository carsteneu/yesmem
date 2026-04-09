package proxy

import "testing"

func TestRulesInjectAfterCollapse_OnlyOnce(t *testing.T) {
	s := &Server{}

	// First collapse injection should return rules
	s.rulesBlock = "test rules"
	result := s.rulesInjectAfterCollapse("thread1", "proj")
	if result == "" {
		t.Fatal("first collapse injection should return rules block")
	}

	// Second collapse injection (same thread) should be blocked
	result = s.rulesInjectAfterCollapse("thread1", "proj")
	if result != "" {
		t.Fatalf("second collapse injection should be empty, got: %s", result)
	}

	// Third collapse — still blocked
	result = s.rulesInjectAfterCollapse("thread1", "proj")
	if result != "" {
		t.Fatal("third collapse injection should still be blocked")
	}
}

func TestRulesInjectAfterCollapse_ResetByNormalInject(t *testing.T) {
	s := &Server{}
	s.rulesBlock = "test rules"

	// Collapse injection
	result := s.rulesInjectAfterCollapse("thread1", "proj")
	if result == "" {
		t.Fatal("first collapse should inject")
	}

	// Normal rulesInject call — sets baseline (first call path)
	result = s.rulesInject("thread1", 100000, "proj")
	// First normal call sets baseline, returns ""
	if result != "" {
		t.Fatal("first normal call should set baseline")
	}

	// Next collapse should work again (flag was reset by normal inject)
	result = s.rulesInjectAfterCollapse("thread1", "proj")
	if result == "" {
		t.Fatal("collapse after normal inject should inject again")
	}
}

func TestRulesInjectAfterCollapse_DifferentThreads(t *testing.T) {
	s := &Server{}
	s.rulesBlock = "test rules"

	// Collapse on thread1
	result := s.rulesInjectAfterCollapse("thread1", "proj")
	if result == "" {
		t.Fatal("thread1 first collapse should inject")
	}

	// Collapse on thread2 should still work (independent)
	result = s.rulesInjectAfterCollapse("thread2", "proj")
	if result == "" {
		t.Fatal("thread2 first collapse should inject independently")
	}

	// thread1 still blocked
	result = s.rulesInjectAfterCollapse("thread1", "proj")
	if result != "" {
		t.Fatal("thread1 second collapse should be blocked")
	}
}

func TestRulesInject_40kInterval(t *testing.T) {
	s := &Server{}
	s.rulesBlock = "test rules"

	// First call sets baseline
	result := s.rulesInject("thread1", 50000, "proj")
	if result != "" {
		t.Fatal("first call should set baseline, not inject")
	}

	// Under 40k delta — no injection
	result = s.rulesInject("thread1", 80000, "proj")
	if result != "" {
		t.Fatal("should not inject at 30k delta")
	}

	// Over 40k delta — inject
	result = s.rulesInject("thread1", 95000, "proj")
	if result == "" {
		t.Fatal("should inject at 45k delta")
	}
}
