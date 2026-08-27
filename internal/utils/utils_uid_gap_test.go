package utils

import (
	"strconv"
	"sync"
	"testing"
)

// -------------------------------------------------------------------
// GenerateUID / SaveUID core shared helper gap tests
// widely used downstream: concurrency + persistence behavior
// -------------------------------------------------------------------

func TestGenerateUID_ReturnsNumericString(t *testing.T) {
	s := GenerateUID()
	if s == "" {
		t.Fatal("GenerateUID returned empty string")
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("GenerateUID result %q is not valid uint64: %v", s, err)
	}
	if n < 100000 {
		t.Fatalf("UID should start from 100000+, actual %d", n)
	}
}

func TestGenerateUID_MonotonicallyIncreasing(t *testing.T) {
	const N = 50
	prev := uint64(0)
	for i := 0; i < N; i++ {
		s := GenerateUID()
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if prev > 0 && n <= prev {
			t.Fatalf("UID not strictly increasing: prev=%d cur=%d (iteration %d)", prev, n, i)
		}
		prev = n
	}
}

func TestGenerateUID_NoDuplicates(t *testing.T) {
	const N = 1000
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		s := GenerateUID()
		if _, ok := seen[s]; ok {
			t.Fatalf("iteration %d produced duplicate UID: %q", i, s)
		}
		seen[s] = struct{}{}
	}
	if len(seen) != N {
		t.Fatalf("expected %d unique values, got %d", N, len(seen))
	}
}

func TestGenerateUID_ConcurrentSafety(t *testing.T) {
	const Goroutines = 20
	const PerGoroutine = 200
	total := Goroutines * PerGoroutine

	results := make(chan string, total)
	var wg sync.WaitGroup
	wg.Add(Goroutines)
	for g := 0; g < Goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < PerGoroutine; i++ {
				results <- GenerateUID()
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[string]struct{}, total)
	count := 0
	for s := range results {
		count++
		if _, ok := seen[s]; ok {
			t.Fatalf("duplicate UID under concurrency: %q", s)
		}
		seen[s] = struct{}{}
	}
	if count != total {
		t.Fatalf("expected %d UIDs, received %d", total, count)
	}
}

func TestGenerateUID_MinimumLength(t *testing.T) {
	s := GenerateUID()
	if len(s) < 6 {
		t.Fatalf("UID length %d should be >= 6, value=%q", len(s), s)
	}
}

func TestSaveUID_DoesNotPanic(t *testing.T) {
	for i := 0; i < 3; i++ {
		_ = GenerateUID()
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SaveUID panicked: %v", r)
		}
	}()
	SaveUID()
}

func TestLoadCounter_AlwaysReturnsAtLeastDefault(t *testing.T) {
	v1 := loadCounter()
	v2 := loadCounter()
	if v1 < 100000 {
		t.Fatalf("loadCounter first return %d, should be >= 100000", v1)
	}
	if v2 != v1 {
		t.Fatalf("loadCounter not stable across calls: v1=%d v2=%d", v1, v2)
	}
}

func TestSaveCounter_InvalidDirNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("saveCounter should silently ignore write errors, panicked: %v", r)
		}
	}()
	saveCounter(123456)
}

func TestGenerateUID_InterleavedWithSaveUID(t *testing.T) {
	const N = 100
	for i := 0; i < N; i++ {
		_ = GenerateUID()
		if i%10 == 0 {
			SaveUID()
		}
	}
	// completes successfully => no race / deadlock
}
