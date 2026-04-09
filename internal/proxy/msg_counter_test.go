package proxy

import "testing"

func TestMsgCounters_FirstCallReturnsLocalValue(t *testing.T) {
	mc := newMsgCounters()
	got := mc.nextFor("t1", 177)
	if got != 177 {
		t.Errorf("first call: want 177, got %d", got)
	}
}

func TestMsgCounters_IncrementsByOneIgnoringLocal(t *testing.T) {
	mc := newMsgCounters()
	mc.nextFor("t1", 100)
	if got := mc.nextFor("t1", 102); got != 101 {
		t.Errorf("second call: want 101, got %d", got)
	}
	if got := mc.nextFor("t1", 104); got != 102 {
		t.Errorf("third call: want 102, got %d", got)
	}
}

func TestMsgCounters_PersistsAcrossCollapse(t *testing.T) {
	mc := newMsgCounters()
	mc.nextFor("t1", 326) // pre-collapse session
	mc.nextFor("t1", 328) // returns 327
	mc.nextFor("t1", 330) // returns 328
	// collapse: local drops from 330 to 60
	got := mc.nextFor("t1", 60)
	if got != 329 {
		t.Errorf("after collapse: want 329 (not 60), got %d", got)
	}
	if got := mc.nextFor("t1", 62); got != 330 {
		t.Errorf("post-collapse+1: want 330, got %d", got)
	}
}

func TestMsgCounters_IndependentPerThread(t *testing.T) {
	mc := newMsgCounters()
	mc.nextFor("t1", 50)
	mc.nextFor("t1", 52) // t1 now at 51
	mc.nextFor("t2", 0)  // t2 starts at 0
	if got := mc.nextFor("t2", 2); got != 1 {
		t.Errorf("t2 second call: want 1, got %d", got)
	}
	if got := mc.nextFor("t1", 0); got != 52 {
		t.Errorf("t1 continues: want 52, got %d", got)
	}
}
