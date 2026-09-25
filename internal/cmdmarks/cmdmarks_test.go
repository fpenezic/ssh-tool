package cmdmarks

import "testing"

func TestBetweenReturnsTheWindowAndForgetsOlderMarks(t *testing.T) {
	var l Log
	l.Add(10, 1)
	l.Add(20, 2)
	l.Add(30, 3)
	l.Add(40, 4)

	got := l.Between(15, 30)
	if len(got) != 2 || got[0].Cum != 20 || got[1].Cum != 30 {
		t.Fatalf("Between(15, 30) = %v, want the marks at 20 and 30", got)
	}
	// The mark at 10 fell out of the ring: it must be gone for good.
	if got := l.Between(0, 100); len(got) != 3 || got[0].Cum != 20 {
		t.Fatalf("after trimming, Between(0, 100) = %v, want 20, 30, 40", got)
	}
}

func TestLogIsBounded(t *testing.T) {
	var l Log
	for i := 0; i < maxMarks+50; i++ {
		l.Add(uint64(i), int64(i))
	}
	got := l.Between(0, ^uint64(0))
	if len(got) != maxMarks || got[0].Cum != 50 {
		t.Fatalf("kept %d marks starting at %d, want %d starting at 50", len(got), got[0].Cum, maxMarks)
	}
}
