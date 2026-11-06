package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

func TestParseConnectorArgs(t *testing.T) {
	ok, err := parseConnectorArgs([]string{"create", "--workspace", "w", "--type", "t", "--name", "n", "--url", "https://x.example.test",
		"--interval", "10m", "--insecure-skip-verify", "--set", "a=b", "--set", "c=1", "--secret-file", "-"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Interval != 10*time.Minute || !ok.InsecureSkipVerify || len(ok.Set) != 2 || ok.SecretFile != "-" || ok.SecretType != "api_token" {
		t.Errorf("parsed %+v", ok)
	}
	bad := [][]string{
		{},
		{"nope"},
		{"list"},
		{"create", "--workspace", "w", "--type", "t"},
		{"create", "--workspace", "w", "--type", "t", "--name", "n", "--secret", "a", "--secret-file", "-"},
		{"create", "--workspace", "w", "--type", "t", "--name", "n", "--interval", "1s"},
		{"sync", "--workspace", "w"},
		{"test", "--workspace", "w", "--connector", "c", "extra"},
		{"create", "--workspace", "w", "--type", "t", "--name", "n", "--secret-value", "x"}, // no secret on argv
	}
	for _, args := range bad {
		if _, err := parseConnectorArgs(args, io.Discard); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
	if _, err := parseConnectorArgs([]string{"types"}, io.Discard); err != nil {
		t.Errorf("types: %v", err)
	}
}

func TestBuildConfig(t *testing.T) {
	spec := connectors.Spec{Fields: []connectors.Field{{Key: "n", Kind: connectors.FieldInt}, {Key: "b", Kind: connectors.FieldBool}, {Key: "s", Kind: connectors.FieldString}}}
	a := connectorArgs{Set: []string{"n=3", "b=true", "s=x=y"}, InsecureSkipVerify: true, TimeoutSeconds: 7, CAFile: "ca.pem"}
	cfg, err := buildConfig(a, spec, func(string) ([]byte, error) { return []byte("PEM"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if cfg["n"] != 3 || cfg["b"] != true || cfg["s"] != "x=y" || cfg["tls_skip_verify"] != true || cfg["timeout_seconds"] != 7 || cfg["ca_pem"] != "PEM" {
		t.Errorf("cfg=%v", cfg)
	}
	for _, bad := range []string{"n=x", "b=maybe", "novalue"} {
		if _, err := buildConfig(connectorArgs{Set: []string{bad}}, spec, nil); err == nil {
			t.Errorf("--set %s accepted", bad)
		}
	}
}

func TestReadSecretValueStdin(t *testing.T) {
	b, err := readSecretValue("-", strings.NewReader("tok\n"))
	if err != nil || string(b) != "tok" {
		t.Errorf("got %q err=%v", b, err)
	}
	if _, err := readSecretValue("-", strings.NewReader("\n")); err == nil {
		t.Error("empty secret accepted")
	}
}
