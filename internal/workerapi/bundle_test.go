package workerapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

func gzipReader(b []byte) (*gzip.Reader, error) { return gzip.NewReader(bytes.NewReader(b)) }

// testRun builds a run row with a sealed parameter snapshot.
func testRun(t *testing.T, ids fixtureIDs, mutate func(*runs.Params)) dbgen.Run {
	t.Helper()
	ws := uuid.New()
	p := runs.Params{
		Playbook: "site.yml", ProjectID: uuid.New(), ProjectRevision: strings.Repeat("ab", 20), InventoryID: uuid.New(),
		Limit: "web", Tags: "setup", Check: true, Verbosity: 2,
		ExtraVars:  map[string]any{"env": "prod", "pw": testSurveyPw},
		SecretVars: []string{"pw"}, EnvVars: map[string]any{"FOO": "bar", "N": float64(3), "B": true},
		SSHSecretID: &ids.ssh, BecomeSecretID: &ids.become, VaultIdentityIDs: []uuid.UUID{ids.vault},
	}
	if mutate != nil {
		mutate(&p)
	}
	if err := p.SealSecretVars(testProvider(t), ws); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return dbgen.Run{ID: uuid.New(), WorkspaceID: ws, Params: raw, LeaseID: pgtype.UUID{Bytes: uuid.New(), Valid: true}}
}

func TestBuildBundle(t *testing.T) {
	ids := fixtureIDs{uuid.New(), uuid.New(), uuid.New()}
	b, trees := testBuilder(t, ids)
	run := testRun(t, ids, nil)
	if strings.Contains(string(run.Params), testSurveyPw) {
		t.Fatal("survey secret stored in plaintext")
	}
	var buf bytes.Buffer
	mask, err := b.Build(context.Background(), run, &buf)
	if err != nil {
		t.Fatal(err)
	}
	files := readTarGz(t, buf.Bytes())
	if trees.rev != strings.Repeat("ab", 20) {
		t.Errorf("rev = %q", trees.rev)
	}
	for _, name := range []string{runproto.FileSpec, runproto.FileInventory, runproto.FileExtraVars, runproto.FileSSHKey,
		runproto.FileBecomePass, runproto.DirVault + "prod", "project/site.yml"} {
		if _, ok := files[name]; !ok {
			t.Errorf("missing %s (have %d files)", name, len(files))
		}
	}
	if _, ok := files["pax_global_header"]; ok {
		t.Error("pax header copied")
	}
	if string(files[runproto.FileSSHKey]) != testKey || string(files[runproto.FileBecomePass]) != testBecome+"\n" ||
		string(files[runproto.DirVault+"prod"]) != testVault+"\n" {
		t.Error("credential files differ")
	}
	var ev map[string]any
	if err := json.Unmarshal(files[runproto.FileExtraVars], &ev); err != nil || ev["pw"] != testSurveyPw || ev["env"] != "prod" {
		t.Errorf("extra vars: %v %v", ev, err)
	}
	if !bytes.Contains(files[runproto.FileInventory], []byte("$ANSIBLE_VAULT")) || bytes.Contains(files[runproto.FileInventory], []byte("<vault>")) {
		t.Error("inventory must carry the unmasked vault value")
	}
	var sp runproto.Spec
	if err := json.Unmarshal(files[runproto.FileSpec], &sp); err != nil {
		t.Fatal(err)
	}
	if sp.Playbook != "site.yml" || sp.Limit != "web" || sp.Tags != "setup" || !sp.Check || sp.Verbosity != 2 ||
		!sp.SSHKey || !sp.Become || len(sp.VaultIDs) != 1 || sp.Env["FOO"] != "bar" || sp.Env["N"] != "3" || sp.Env["B"] != "true" {
		t.Errorf("spec: %+v", sp)
	}
	for _, want := range []string{testKey, testBecome, testVault, testSurveyPw} {
		found := false
		for _, m := range mask {
			found = found || strings.TrimSpace(m) == strings.TrimSpace(want)
		}
		if !found {
			t.Errorf("mask list lacks a credential")
		}
	}
}

func TestBuildBundleMissingVaultPasswordOrWrongType(t *testing.T) {
	ids := fixtureIDs{uuid.New(), uuid.New(), uuid.New()}
	b, _ := testBuilder(t, ids)
	// A password secret used as SSH key is rejected as a configuration error.
	bad := testRun(t, ids, func(p *runs.Params) { p.SSHSecretID = &ids.become })
	if _, err := b.Build(context.Background(), bad, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not an ssh_key") {
		t.Errorf("err = %v", err)
	}
	// After the run ended the envelope is gone and the masked placeholder remains.
	done := testRun(t, ids, nil)
	p, _ := runs.DecodeParams(done.Params)
	p.SecretVarsEnc = nil
	done.Params, _ = json.Marshal(p)
	var buf bytes.Buffer
	if _, err := b.Build(context.Background(), done, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readTarGz(t, buf.Bytes())[runproto.FileExtraVars]), testSurveyPw) {
		t.Error("wiped run still has the value")
	}
}

func TestBuildBundleUnsupportedEnvType(t *testing.T) {
	ids := fixtureIDs{uuid.New(), uuid.New(), uuid.New()}
	b, _ := testBuilder(t, ids)
	run := testRun(t, ids, func(p *runs.Params) { p.EnvVars = map[string]any{"X": []any{1}} })
	if _, err := b.Build(context.Background(), run, &bytes.Buffer{}); err == nil {
		t.Error("expected error")
	}
}
