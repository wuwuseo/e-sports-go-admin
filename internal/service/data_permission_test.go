package service

import "testing"

func TestContainsInt_Found(t *testing.T) {
	if !containsInt([]int{1, 2, 3}, 2) {
		t.Fatal("期望 true")
	}
}

func TestContainsInt_NotFound(t *testing.T) {
	if containsInt([]int{1, 2, 3}, 99) {
		t.Fatal("期望 false")
	}
}

func TestContainsInt_EmptySlice(t *testing.T) {
	if containsInt(nil, 1) {
		t.Fatal("期望 false")
	}
}
