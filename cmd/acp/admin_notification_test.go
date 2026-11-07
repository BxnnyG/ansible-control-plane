package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestParseNotificationArgs(t *testing.T) {
	ok, err := parseNotificationArgs([]string{"channel", "create", "--workspace", "w", "--type", "smtp", "--name", "mail",
		"--set", "host=smtp.example.test", "--set", "port=587", "--secret-file", "-", "--secret-type", "password", "--disabled"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Type != "smtp" || len(ok.Set) != 2 || ok.SecretFile != "-" || !ok.Disabled || ok.SecretType != "password" {
		t.Errorf("parsed %+v", ok)
	}
	u, err := parseNotificationArgs([]string{"channel", "update", "--workspace", "w", "--channel", "mail", "--insecure-skip-verify=false", "--disable"}, io.Discard)
	if err != nil || !u.Given["insecure-skip-verify"] || u.InsecureSkipVerify || !u.Disable {
		t.Errorf("update: %v %+v", err, u)
	}
	r, err := parseNotificationArgs([]string{"rule", "create", "--workspace", "w", "--channel", "mail", "--event", "run.failed", "--event", "run.recovered"}, io.Discard)
	if err != nil || len(r.Events) != 2 {
		t.Errorf("rule: %v %+v", err, r)
	}
	if _, err := parseNotificationArgs([]string{"delivery", "list", "--workspace", "w", "--channel", "c", "--status", "failed", "--limit", "5"}, io.Discard); err != nil {
		t.Errorf("delivery: %v", err)
	}

	bad := [][]string{
		{},
		{"channel"},
		{"nope", "list"},
		{"channel", "list"},
		{"channel", "create", "--workspace", "w", "--type", "smtp"},
		{"channel", "create", "--workspace", "w", "--type", "pigeon", "--name", "n"},
		{"channel", "create", "--workspace", "w", "--type", "ntfy", "--name", "n", "--secret", "a", "--secret-file", "-"},
		{"channel", "create", "--workspace", "w", "--type", "ntfy", "--name", "n", "--config-file", "-", "--secret-file", "-"},
		{"channel", "create", "--workspace", "w", "--type", "ntfy", "--name", "n", "--set", "novalue"},
		{"channel", "create", "--workspace", "w", "--type", "ntfy", "--name", "n", "--secret-value", "x"}, // no secret on argv
		{"channel", "update", "--workspace", "w", "--channel", "c", "--enable", "--disable"},
		{"channel", "update", "--workspace", "w", "--channel", "c", "--clear-secret", "--secret", "s"},
		{"channel", "test", "--workspace", "w"},
		{"channel", "delete", "--workspace", "w", "--channel", "c", "extra"},
		{"rule", "create", "--workspace", "w", "--channel", "c"},
		{"rule", "create", "--workspace", "w", "--channel", "c", "--event", "run.exploded"},
		{"rule", "delete", "--workspace", "w", "--rule", "not-a-uuid"},
		{"delivery", "list", "--workspace", "w", "--channel", "c", "--status", "weird"},
		{"delivery", "list", "--workspace", "w", "--channel", "c", "--limit", "999"},
	}
	for _, args := range bad {
		if _, err := parseNotificationArgs(args, io.Discard); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
}

func TestApplyConfigFlags(t *testing.T) {
	a := notificationArgs{Set: []string{"port=587", "host=smtp.example.test", "to=[\"a@example.test\"]"}, Unset: []string{"old"}}
	base := json.RawMessage(`{"old":1,"from":"x@example.test"}`)
	out, err := applyConfigFlags(a, base, strings.NewReader(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["port"] != float64(587) || m["host"] != "smtp.example.test" || m["from"] != "x@example.test" || m["old"] != nil {
		t.Errorf("config %v", m)
	}
	if to, _ := m["to"].([]any); len(to) != 1 {
		t.Errorf("to %v", m["to"])
	}
	// A config file replaces the stored config; stdin works.
	out, err = applyConfigFlags(notificationArgs{ConfigFile: "-"}, base, strings.NewReader(`{"url":"https://x.example.test"}`), nil)
	if err != nil || string(out) != `{"url":"https://x.example.test"}` {
		t.Errorf("stdin config: %v %s", err, out)
	}
	if _, err := applyConfigFlags(notificationArgs{ConfigFile: "-"}, nil, strings.NewReader(`[1]`), nil); err == nil {
		t.Error("non-object config accepted")
	}
}
