package cron

import (
	"testing"
)

func TestRegistryRegisterAndGet(t *testing.T) {
	defer func() { Registry = map[string]RegisteredCommand{} }()

	Register("test_cmd", "/usr/bin/test", 3, "test command")
	cmd, ok := Get("test_cmd")
	if !ok {
		t.Fatal("Get() returned false for registered command")
	}
	if cmd.Path != "/usr/bin/test" {
		t.Fatalf("Path = %q, want /usr/bin/test", cmd.Path)
	}
	if cmd.MaxArgs != 3 {
		t.Fatalf("MaxArgs = %d, want 3", cmd.MaxArgs)
	}
	if cmd.Description != "test command" {
		t.Fatalf("Description = %q", cmd.Description)
	}
}

func TestRegistryRegisterWithArgs(t *testing.T) {
	defer func() { Registry = map[string]RegisteredCommand{} }()

	RegisterWithArgs("prefix_cmd", "/usr/bin/echo", []string{"prefix"}, 2, "echo with prefix")
	cmd, ok := Get("prefix_cmd")
	if !ok {
		t.Fatal("Get() returned false")
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "prefix" {
		t.Fatalf("Args = %+v", cmd.Args)
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	defer func() { Registry = map[string]RegisteredCommand{} }()

	_, ok := Get("nonexistent")
	if ok {
		t.Fatal("Get() returned true for unknown command")
	}
}

func TestRegistryIsRegistered(t *testing.T) {
	defer func() { Registry = map[string]RegisteredCommand{} }()

	if IsRegistered("not_yet") {
		t.Fatal("IsRegistered() returned true before registration")
	}
	Register("check_cmd", "/bin/true", 0, "check")
	if !IsRegistered("check_cmd") {
		t.Fatal("IsRegistered() returned false after registration")
	}
}

func TestRegistryList(t *testing.T) {
	defer func() { Registry = map[string]RegisteredCommand{} }()

	if len(List()) != 0 {
		t.Fatalf("List() = %d, want 0", len(List()))
	}
	Register("cmd_a", "/a", 0, "a")
	Register("cmd_b", "/b", 0, "b")
	names := List()
	if len(names) != 2 {
		t.Fatalf("List() len = %d, want 2", len(names))
	}
	hasA, hasB := false, false
	for _, n := range names {
		if n == "cmd_a" {
			hasA = true
		}
		if n == "cmd_b" {
			hasB = true
		}
	}
	if !hasA || !hasB {
		t.Fatalf("List() = %+v", names)
	}
}

func TestValidateArgsWithinLimit(t *testing.T) {
	cmd := &RegisteredCommand{MaxArgs: 3}
	if err := cmd.ValidateArgs([]string{"a", "b"}); err != nil {
		t.Fatalf("ValidateArgs(2 args) error = %v", err)
	}
	if err := cmd.ValidateArgs([]string{"a", "b", "c"}); err != nil {
		t.Fatalf("ValidateArgs(3 args) error = %v", err)
	}
}

func TestValidateArgsExceedsLimit(t *testing.T) {
	cmd := &RegisteredCommand{MaxArgs: 2}
	err := cmd.ValidateArgs([]string{"a", "b", "c"})
	if err == nil {
		t.Fatal("ValidateArgs() accepted too many args")
	}
}

func TestValidateArgsZeroMeansUnlimited(t *testing.T) {
	cmd := &RegisteredCommand{MaxArgs: 0}
	if err := cmd.ValidateArgs([]string{"a", "b", "c", "d", "e"}); err != nil {
		t.Fatalf("ValidateArgs(MaxArgs=0) error = %v", err)
	}
}

func TestValidateArgsEmptyArgs(t *testing.T) {
	cmd := &RegisteredCommand{MaxArgs: 5}
	if err := cmd.ValidateArgs(nil); err != nil {
		t.Fatalf("ValidateArgs(nil) error = %v", err)
	}
	if err := cmd.ValidateArgs([]string{}); err != nil {
		t.Fatalf("ValidateArgs(empty) error = %v", err)
	}
}
