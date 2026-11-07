package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/runmask"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

const (
	heartbeatInterval = 5 * time.Second
	// heartbeatGiveUp: without a successful heartbeat for this long the app has
	// declared the run lost (60 s); the worker stops the process too.
	heartbeatGiveUp = 45 * time.Second
	killGrace       = 10 * time.Second
)

// Runner executes claimed runs.
type Runner struct {
	API      API
	Log      *slog.Logger
	WorkDir  string // run directories live here (tmpfs), default /tmp/runs
	CacheDir string // Galaxy cache
	// Binaries; defaults "ansible-playbook" and "ansible-galaxy".
	PlaybookBin string
	GalaxyBin   string
	// Environ is the process environment passed through selectively (default os.Environ()).
	Environ []string
	// KillGrace overrides the SIGTERM->SIGKILL delay (tests).
	KillGrace time.Duration
	// HeartbeatEvery overrides the heartbeat period (tests).
	HeartbeatEvery time.Duration

	// JobUIDBase is the first uid of the per-slot job users (ADR-0020); 0 runs
	// jobs under the supervisor uid.
	JobUIDBase int

	slotMu   sync.Mutex
	slotBusy map[int]bool
}

func (r *Runner) playbookBin() string {
	if r.PlaybookBin != "" {
		return r.PlaybookBin
	}
	return "ansible-playbook"
}

func (r *Runner) galaxyBin() string {
	if r.GalaxyBin != "" {
		return r.GalaxyBin
	}
	return "ansible-galaxy"
}

func (r *Runner) workDir() string {
	if r.WorkDir != "" {
		return r.WorkDir
	}
	return "/tmp/runs"
}

func (r *Runner) environ() []string {
	if r.Environ != nil {
		return r.Environ
	}
	return os.Environ()
}

func (r *Runner) killGrace() time.Duration {
	if r.KillGrace > 0 {
		return r.KillGrace
	}
	return killGrace
}

// Execute runs one claimed run to completion and reports the result. ctx
// ending (worker shutdown) cancels the run. It never logs secret values.
func (r *Runner) Execute(ctx context.Context, claim runproto.ClaimResponse) {
	log := r.Log.With("run", claim.RunID)
	// Reporting must survive a cancelled ctx (shutdown).
	rctx := context.WithoutCancel(ctx)

	slot, releaseSlot := r.acquireSlot()
	defer releaseSlot()
	job := r.ident(slot)

	dir := filepath.Join(r.workDir(), claim.RunID)
	workMode := os.FileMode(0o700)
	if job != nil {
		workMode = 0o711 // job users must traverse to their own run directory
	}
	if err := os.MkdirAll(r.workDir(), workMode); err != nil {
		log.Error("create work dir", "error", err)
	}
	_ = os.Chmod(r.workDir(), workMode)
	defer func() { removeRun(dir, job) }()

	st := &streamer{api: r.API, run: claim.RunID, lease: claim.LeaseID, log: log, masker: runmask.New(nil), evPath: filepath.Join(dir, eventsFile)}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	ctl := &control{}
	var leaseLost bool
	var lostMu sync.Mutex
	st.onLeaseLost = func() {
		lostMu.Lock()
		leaseLost = true
		lostMu.Unlock()
		stop()
	}
	fail := func(msg string, err error) {
		log.Error(msg, "error", err)
		st.Println("[acp] " + msg + ": " + err.Error())
		_ = st.Flush(rctx, true)
		if ferr := withRetry(rctx, sendBudget, func(c context.Context) error {
			return r.API.Finish(c, claim.RunID, claim.LeaseID, runproto.FinishRequest{ExitCode: -1})
		}); ferr != nil && !errors.Is(ferr, ErrLeaseLost) {
			log.Error("report failure", "error", ferr)
		}
	}

	// Heartbeat from the start: fetching the bundle and installing Galaxy
	// requirements can take longer than the 60 s liveness threshold.
	hbCtx, hbStop := context.WithCancel(rctx)
	hbDone := make(chan struct{})
	defer func() { hbStop(); <-hbDone }()
	go func() {
		defer close(hbDone)
		r.heartbeats(hbCtx, ctx, log, claim, st, ctl, stop)
	}()
	go st.loop(hbCtx)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail("cannot create run directory", err)
		return
	}
	body, err := r.API.Bundle(rctx, claim.RunID, claim.LeaseID)
	if err != nil {
		fail("cannot fetch bundle", err)
		return
	}
	err = extract(body, dir)
	_ = body.Close()
	if err != nil {
		fail("cannot unpack bundle", err)
		return
	}
	sp, err := readSpec(dir)
	if err != nil {
		fail("invalid bundle", err)
		return
	}
	st.masker = runmask.New(sp.Mask)
	if err := prepareDirs(dir, sp); err != nil {
		fail("cannot prepare run directory", err)
		return
	}
	if err := handOver(dir, job); err != nil {
		fail("cannot hand over run directory", err)
		return
	}

	exit, cancelled := r.run(runCtx, log, sp, dir, job, st, ctl)

	lostMu.Lock()
	lost := leaseLost
	lostMu.Unlock()
	if lost {
		log.Warn("lease lost, run abandoned")
		return
	}
	_ = st.Flush(rctx, true)
	err = withRetry(rctx, sendBudget, func(c context.Context) error {
		return r.API.Finish(c, claim.RunID, claim.LeaseID, runproto.FinishRequest{ExitCode: exit, Cancelled: cancelled})
	})
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		log.Error("report finish", "error", err)
	}
}

func readSpec(dir string) (runproto.Spec, error) {
	var sp runproto.Spec
	b, err := os.ReadFile(filepath.Join(dir, runproto.FileSpec)) //nolint:gosec // fixed name in our own run dir
	if err != nil {
		return sp, err
	}
	if err := json.Unmarshal(b, &sp); err != nil {
		return sp, fmt.Errorf("decode spec: %w", err)
	}
	return sp, nil
}

func prepareDirs(dir string, sp runproto.Spec) error {
	for _, d := range []string{filepath.Join(dir, tmpSubdir, "local"), filepath.Join(dir, tmpSubdir, "home"), filepath.Join(dir, tmpSubdir, "remote"), filepath.Join(dir, projectDir)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := validPlaybook(sp.Playbook); err != nil {
		return err
	}
	if err := insideProject(filepath.Join(dir, projectDir), sp.Playbook); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, eventsFile), nil, 0o600); err != nil {
		return err
	}
	return writeInventoryScript(dir)
}

// control carries the cancellation state shared by the heartbeat loop and
// the process runner.
type control struct {
	mu        sync.Mutex
	cancelled bool
	term      func()
}

// Cancel marks the run cancelled; it reports whether this was the first call.
func (c *control) Cancel() bool {
	c.mu.Lock()
	first := !c.cancelled
	c.cancelled = true
	term := c.term
	c.mu.Unlock()
	if first && term != nil {
		term()
	}
	return first
}

// SetTerm registers the function that stops the process; it runs at once if
// the run was cancelled in the meantime.
func (c *control) SetTerm(f func()) {
	c.mu.Lock()
	c.term = f
	cancelled := c.cancelled
	c.mu.Unlock()
	if cancelled {
		f()
	}
}

func (c *control) Cancelled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelled
}

// heartbeats reports liveness every heartbeatInterval and turns a cancel
// request, worker shutdown (runCtx) or a lost lease into a stop of the run.
func (r *Runner) heartbeats(hbCtx, runCtx context.Context, log *slog.Logger, claim runproto.ClaimResponse, st *streamer, ctl *control, stopPhase func()) {
	every := heartbeatInterval
	if r.HeartbeatEvery > 0 {
		every = r.HeartbeatEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	lastOK := time.Now()
	cancel := func(why string) {
		if ctl.Cancel() {
			log.Info("stopping run", "reason", why)
			st.Println("[acp] run cancelled (" + why + ")")
			stopPhase()
		}
	}
	for {
		select {
		case <-hbCtx.Done():
			return
		case <-runCtx.Done():
			cancel("worker shutdown")
			<-hbCtx.Done()
			return
		case <-t.C:
		}
		c, err := r.API.Heartbeat(hbCtx, claim.RunID, claim.LeaseID)
		switch {
		case errors.Is(err, ErrLeaseLost):
			st.onLeaseLost()
			cancel("lease lost")
			return
		case err != nil:
			if hbCtx.Err() != nil {
				return
			}
			log.Warn("heartbeat failed", "error", err)
			if time.Since(lastOK) > heartbeatGiveUp {
				cancel("app unreachable")
			}
		default:
			lastOK = time.Now()
			if c {
				cancel("requested")
			}
		}
	}
}

// run installs requirements and executes the playbook; it returns the exit
// code and whether the run was cancelled.
func (r *Runner) run(ctx context.Context, log *slog.Logger, sp runproto.Spec, dir string, job *jobIdent, st *streamer, ctl *control) (int, bool) {
	project := filepath.Join(dir, projectDir)
	var collections, roles string
	baseEnv := buildEnv(sp, dir, r.environ(), "", "")
	if gc, err := r.installRequirements(ctx, project, baseEnv, job, st); err != nil {
		if ctl.Cancelled() {
			return -1, true
		}
		st.Println("[acp] installing Galaxy requirements failed: " + err.Error())
		return -1, false
	} else if gc != nil {
		collections = gc.Collections
		roles = gc.Roles + ":" + filepath.Join(project, "roles")
	}
	if ctl.Cancelled() {
		return -1, true
	}
	args, err := buildArgs(sp, dir)
	if err != nil {
		st.Println("[acp] " + err.Error())
		return -1, false
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), r.playbookBin(), args...) //nolint:gosec // argv built from validated spec, no shell
	cmd.Dir = project
	cmd.Env = buildEnv(sp, dir, r.environ(), collections, roles)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: job.credential()}
	pr, pw, err := os.Pipe()
	if err != nil {
		st.Println("[acp] cannot create output pipe: " + err.Error())
		return -1, false
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		st.Println("[acp] cannot start ansible-playbook: " + err.Error())
		return -1, false
	}
	_ = pw.Close()
	log.Info("playbook started", "pid", cmd.Process.Pid)

	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(st, pr)
		_ = pr.Close()
		close(copied)
	}()

	exited := make(chan struct{})
	ctl.SetTerm(func() {
		st.Println("[acp] sending SIGTERM to ansible-playbook")
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		go func() {
			select {
			case <-exited:
			case <-time.After(r.killGrace()):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}()
	})

	waitErr := cmd.Wait()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // stragglers of the process group
	close(exited)
	<-copied
	cancelled := ctl.Cancelled()
	if waitErr == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return ee.ExitCode(), cancelled // -1 when killed by a signal
	}
	st.Println("[acp] ansible-playbook failed: " + waitErr.Error())
	return -1, cancelled
}
