package worker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Job process isolation (ADR-0020, finding F-02). The worker supervisor runs
// as the acp user and owns the worker token; ansible-playbook and everything
// it spawns run under a per-slot job uid (JobUIDBase+slot) that cannot read
// the token, the supervisor's /proc entries or the run directory of another
// slot. The supervisor holds CAP_SETUID, CAP_SETGID, CAP_CHOWN and CAP_KILL
// through file capabilities on its own binary (the binary is not executable
// for job uids, and a job process never inherits capabilities).

// Capability numbers (linux/capability.h).
const (
	capChown  = 0
	capKill   = 5
	capSetgid = 6
	capSetuid = 7
)

// checkCapabilities fails unless the effective set holds the capabilities
// needed to start and manage jobs under another uid.
func checkCapabilities() error {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		v, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		eff, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
		if err != nil {
			return fmt.Errorf("parse CapEff: %w", err)
		}
		var missing []string
		for name, c := range map[string]uint{"CAP_CHOWN": capChown, "CAP_KILL": capKill, "CAP_SETGID": capSetgid, "CAP_SETUID": capSetuid} {
			if eff&(1<<c) == 0 {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("job isolation needs effective capabilities %s (file capabilities on the worker binary and cap_add in the container); set ACP_WORKER_JOB_UID_BASE=0 to run jobs unisolated", strings.Join(missing, ", "))
		}
		return nil
	}
	return errors.New("CapEff not found in /proc/self/status")
}

// slotAlloc hands out the lowest free job slot.
func (r *Runner) acquireSlot() (int, func()) {
	r.slotMu.Lock()
	defer r.slotMu.Unlock()
	if r.slotBusy == nil {
		r.slotBusy = map[int]bool{}
	}
	n := 0
	for r.slotBusy[n] {
		n++
	}
	r.slotBusy[n] = true
	return n, func() {
		r.slotMu.Lock()
		delete(r.slotBusy, n)
		r.slotMu.Unlock()
	}
}

// jobIdent is the uid/gid a job runs as; a nil *jobIdent means "same as the supervisor".
type jobIdent struct{ uid int }

func (j *jobIdent) credential() *syscall.Credential {
	if j == nil {
		return nil
	}
	return &syscall.Credential{Uid: uint32(j.uid), Gid: uint32(j.uid), Groups: []uint32{}} //nolint:gosec // validated range
}

// ident returns the identity for a slot (nil when isolation is off).
func (r *Runner) ident(slot int) *jobIdent {
	if r.JobUIDBase == 0 {
		return nil
	}
	return &jobIdent{uid: r.JobUIDBase + slot}
}

// handOver gives a prepared run directory to the job user: the directory
// itself stays supervisor-owned (mode 0710, group = job gid) so the job can
// traverse but nobody else; everything below belongs to the job. The project
// tree and events.jsonl keep the supervisor's group with group-read so the
// supervisor can read requirement files and tail the callback events.
func handOver(dir string, j *jobIdent) error {
	if j == nil {
		return nil
	}
	self := os.Getegid()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := chownTree(root, ".", func(rel string, mode fs.FileMode) (int, fs.FileMode) {
		switch {
		case rel == eventsFile:
			return self, 0o640
		case rel == projectDir || strings.HasPrefix(rel, projectDir+"/"):
			return self, mode.Perm() | (mode.Perm()&0o500)>>3
		}
		return j.uid, mode.Perm()
	}, j.uid); err != nil {
		return fmt.Errorf("hand over run directory: %w", err)
	}
	if err := os.Chown(dir, os.Geteuid(), j.uid); err != nil {
		return err
	}
	return os.Chmod(dir, 0o710) //nolint:gosec // run dir must be traversable by the job group
}

// chownTree chowns everything below rel (post-order, so the supervisor can
// still read directories while descending) to uid and the group/mode chosen
// by pick. Symlinks are chowned themselves, never followed.
func chownTree(root *os.Root, rel string, pick func(rel string, m fs.FileMode) (int, fs.FileMode), uid int) error {
	fi, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		f, err := root.Open(rel)
		if err != nil {
			return err
		}
		ents, err := f.ReadDir(-1)
		_ = f.Close()
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := chownTree(root, joinRel(rel, e.Name()), pick, uid); err != nil {
				return err
			}
		}
	}
	if rel == "." {
		return nil
	}
	gid, mode := pick(rel, fi.Mode())
	// chmod first: after the chown the supervisor is no longer the owner.
	if fi.Mode()&fs.ModeSymlink == 0 {
		if err := root.Chmod(rel, mode); err != nil {
			return err
		}
	}
	return root.Lchown(rel, uid, gid)
}

func joinRel(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// reclaim takes a tree back from the job user (top-down, so each directory is
// readable before it is listed), optionally making it world-readable. The
// job processes must be gone; it is called after the process group ended.
func reclaim(dir string, j *jobIdent, readable bool) error {
	if j == nil {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	self := os.Geteuid()
	var walk func(rel string) error
	walk = func(rel string) error {
		fi, err := root.Lstat(rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := root.Lchown(rel, self, os.Getegid()); err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		perm := fi.Mode().Perm()
		switch {
		case fi.IsDir() && readable:
			perm = 0o755
		case fi.IsDir():
			perm |= 0o700
		case readable && perm&0o100 != 0:
			perm = 0o755
		case readable:
			perm = 0o644
		}
		if err := root.Chmod(rel, perm); err != nil {
			return err
		}
		if !fi.IsDir() {
			return nil
		}
		f, err := root.Open(rel)
		if err != nil {
			return err
		}
		ents, err := f.ReadDir(-1)
		_ = f.Close()
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := walk(joinRel(rel, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(".")
}

// removeRun deletes a run directory after taking it back from the job user.
func removeRun(dir string, j *jobIdent) {
	_ = reclaim(dir, j, false)
	_ = os.RemoveAll(dir)
}

// insideProject reports whether the playbook, with symlinks resolved, stays in
// the project directory (a missing file is left to ansible to report).
func insideProject(project, playbook string) error {
	base, err := filepath.EvalSymlinks(project)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(project, playbook))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
		return fmt.Errorf("playbook %q resolves outside the project", playbook)
	}
	return nil
}

// cacheMode is the mode of the Galaxy cache directories: job users must be
// able to traverse them to read installed content, but not to list or write.
func cacheMode(j *jobIdent) os.FileMode {
	if j == nil {
		return 0o700
	}
	return 0o711
}

// giveTree hands a freshly created directory tree to the job user (Galaxy
// install target); reclaim(..., true) takes it back, world-readable.
func giveTree(dir string, j *jobIdent) error {
	if j == nil {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := chownTree(root, ".", func(_ string, mode fs.FileMode) (int, fs.FileMode) { return j.uid, mode.Perm() }, j.uid); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // job user owns the install target
		return err
	}
	return os.Chown(dir, j.uid, j.uid)
}
