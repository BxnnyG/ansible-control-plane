package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

// fakeAnsible writes a script that stands in for ansible-playbook. It records
// its argv and environment next to itself and then runs body.
func fakeAnsible(t *testing.T, body string) (bin, outDir string) {
	t.Helper()
	outDir = t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + outDir + "/argv'\n" +
		"env > '" + outDir + "/env'\n" +
		"pwd > '" + outDir + "/pwd'\n" + body
	bin = filepath.Join(outDir, "ansible-playbook")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	return bin, outDir
}

func newTestRunner(t *testing.T, api *fakeAPI, bin string) *Runner {
	t.Helper()
	return &Runner{
		API: api, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), WorkDir: t.TempDir(), CacheDir: t.TempDir(),
		PlaybookBin: bin, Environ: []string{"PATH=/usr/bin:/bin"}, KillGrace: 300 * time.Millisecond, HeartbeatEvery: 50 * time.Millisecond,
	}
}

func testSpec(mask ...string) runproto.Spec {
	return runproto.Spec{RunID: "r1", Playbook: "site.yml", Limit: "web", SSHKey: true, Mask: mask, Env: map[string]string{"FOO": "bar"}}
}

func bundleFor(t *testing.T, sp runproto.Spec) []byte {
	t.Helper()
	spec, _ := json.Marshal(sp)
	return makeBundle(t, map[string]string{
		"spec.json": string(spec), "inventory.json": `{"_meta":{"hostvars":{}},"all":{"children":["ungrouped"]}}`,
		"extra_vars.json": `{"a":1}`, "secrets/ssh_key": "KEYDATA", "project/site.yml": "- hosts: all\n",
	})
}

var claim = runproto.ClaimResponse{RunID: "run-1", LeaseID: "lease-1"}

func TestExecuteSuccess(t *testing.T) {
	bin, out := fakeAnsible(t, `
echo "PLAY [all] with pw s3cretpw"
echo '{"event":"runner_changed","host":"h1","task":"t1"}' >> "$ACP_EVENTS_FILE"
echo '{"event":"stats","hosts":{"h1":{"ok":1,"changed":1}}}' >> "$ACP_EVENTS_FILE"
echo "err line" >&2
exit 0
`)
	api := &fakeAPI{bundle: bundleFor(t, testSpec("s3cretpw"))}
	r := newTestRunner(t, api, bin)
	r.Execute(context.Background(), claim)

	if api.finish == nil || api.finish.ExitCode != 0 || api.finish.Cancelled {
		t.Fatalf("finish = %+v", api.finish)
	}
	lg := api.log()
	if !strings.Contains(lg, "PLAY [all] with pw ***") || !strings.Contains(lg, "err line") || strings.Contains(lg, "s3cretpw") {
		t.Errorf("log = %q", lg)
	}
	if len(api.events) != 2 || !strings.Contains(string(api.events[0]), "runner_changed") {
		t.Errorf("events = %s", api.events)
	}
	argv, _ := os.ReadFile(filepath.Join(out, "argv"))
	for _, want := range []string{"--private-key", "--limit\nweb", "-e\n@", "--\nsite.yml"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("argv lacks %q:\n%s", want, argv)
		}
	}
	env, _ := os.ReadFile(filepath.Join(out, "env"))
	for _, want := range []string{"FOO=bar", "ANSIBLE_CALLBACKS_ENABLED=acp", "ACP_EVENTS_FILE=", "ANSIBLE_FORCE_COLOR=1"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("env lacks %q", want)
		}
	}
	if pwd, _ := os.ReadFile(filepath.Join(out, "pwd")); !strings.HasSuffix(strings.TrimSpace(string(pwd)), "/project") {
		t.Errorf("cwd = %q", pwd)
	}
	if _, err := os.Stat(filepath.Join(r.WorkDir, claim.RunID)); !os.IsNotExist(err) {
		t.Error("run directory not removed")
	}
}

func TestExecuteFailureExitCode(t *testing.T) {
	bin, _ := fakeAnsible(t, "echo boom\nexit 2\n")
	api := &fakeAPI{bundle: bundleFor(t, testSpec())}
	newTestRunner(t, api, bin).Execute(context.Background(), claim)
	if api.finish == nil || api.finish.ExitCode != 2 || api.finish.Cancelled {
		t.Fatalf("finish = %+v", api.finish)
	}
}

func TestExecuteCancelSigterm(t *testing.T) {
	bin, _ := fakeAnsible(t, "echo started\nsleep 30\n")
	api := &fakeAPI{bundle: bundleFor(t, testSpec()), cancelAt: 3}
	start := time.Now()
	newTestRunner(t, api, bin).Execute(context.Background(), claim)
	if api.finish == nil || !api.finish.Cancelled {
		t.Fatalf("finish = %+v", api.finish)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("cancel took %v", d)
	}
	if !strings.Contains(api.log(), "started") {
		t.Errorf("log = %q", api.log())
	}
}

func TestExecuteCancelEscalatesToSigkill(t *testing.T) {
	bin, _ := fakeAnsible(t, "trap '' TERM\necho started\nwhile true; do sleep 1; done\n")
	api := &fakeAPI{bundle: bundleFor(t, testSpec()), cancelAt: 2}
	start := time.Now()
	newTestRunner(t, api, bin).Execute(context.Background(), claim)
	if api.finish == nil || !api.finish.Cancelled || api.finish.ExitCode == 0 {
		t.Fatalf("finish = %+v", api.finish)
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("kill took %v", d)
	}
}

func TestExecuteLeaseLostAbandonsRun(t *testing.T) {
	bin, _ := fakeAnsible(t, "sleep 30\n")
	api := &fakeAPI{bundle: bundleFor(t, testSpec()), leaseLost: true}
	newTestRunner(t, api, bin).Execute(context.Background(), claim)
	if api.finish != nil {
		t.Errorf("finish reported after lease loss: %+v", api.finish)
	}
}

func TestExecuteShutdownCancelsAndReports(t *testing.T) {
	bin, _ := fakeAnsible(t, "sleep 30\n")
	api := &fakeAPI{bundle: bundleFor(t, testSpec())}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	newTestRunner(t, api, bin).Execute(ctx, claim)
	if api.finish == nil || !api.finish.Cancelled {
		t.Fatalf("finish = %+v", api.finish)
	}
}

func TestExecuteBadBundleFails(t *testing.T) {
	api := &fakeAPI{bundle: []byte("not a gzip")}
	newTestRunner(t, api, "/bin/true").Execute(context.Background(), claim)
	if api.finish == nil || api.finish.ExitCode != -1 || !strings.Contains(api.log(), "cannot unpack bundle") {
		t.Fatalf("finish = %+v log=%q", api.finish, api.log())
	}
}

func TestExecuteUnsafePlaybookFails(t *testing.T) {
	sp := testSpec()
	sp.Playbook = "../../etc/x.yml"
	api := &fakeAPI{bundle: bundleFor(t, sp)}
	newTestRunner(t, api, "/bin/true").Execute(context.Background(), claim)
	if api.finish == nil || api.finish.ExitCode != -1 {
		t.Fatalf("finish = %+v", api.finish)
	}
}
