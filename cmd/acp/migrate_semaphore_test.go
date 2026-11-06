package main

import (
	"io"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

func TestParseMigrateArgs(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "ACP_MIGRATE_SOURCE_DSN" {
			return "u:p@tcp(h)/d", true
		}
		return "", false
	}
	a, err := parseMigrateArgs([]string{"semaphore", "--backup-dir", "/b", "--inventory-map", "3=prod", "--inventory-map=4=lab"}, env, io.Discard)
	if err != nil || a.DSN == "" || a.Workspace != "default" || a.InventoryMap[3] != "prod" || a.InventoryMap[4] != "lab" || !a.DryRun {
		t.Fatalf("ok case: %+v %v", a, err)
	}
	for _, args := range [][]string{
		{"semaphore", "--backup-dir", "/b", "--source-dsn", "x", "--apply", "--json"},
		{"semaphore", "--backup-dir", "/b", "--source-dsn", "x", "--compare", "--check-runs"},
	} {
		a, err := parseMigrateArgs(args, noEnv, io.Discard)
		if err != nil || a.DryRun || (!a.Apply && !a.Compare) {
			t.Errorf("%v: %+v %v", args, a, err)
		}
	}
	bad := [][]string{
		{},
		{"other"},
		{"semaphore"},
		{"semaphore", "--backup-dir", "/b"},
		{"semaphore", "--backup-dir", "/b", "--source-dsn", "x", "--inventory-map", "nope"},
		{"semaphore", "--backup-dir", "/b", "--source-dsn", "x", "--dry-run=false"},
		{"semaphore", "--backup-dir", "/b", "--source-dsn", "x", "--apply", "--compare"},
		{"semaphore", "--backup-dir", "/b", "--source-dsn", "x", "--apply", "--dry-run"},
		{"semaphore", "--backup-dir", "/b", "--source-dsn", "x", "--check-runs"},
	}
	for _, args := range bad {
		if _, err := parseMigrateArgs(args, noEnv, io.Discard); err == nil {
			t.Errorf("expected error for %v", args)
		}
	}
	// The DSN must never show up in an error.
	_, err = parseMigrateArgs([]string{"semaphore", "--source-dsn", "user:hunter2@tcp(h)/d", "--inventory-map", "x"}, noEnv, io.Discard)
	if err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Fatal("dsn leaked")
	}
}
