package semaphore

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Backup is a verified backup directory created by scripts/backup-semaphore.sh.
type Backup struct {
	Dir string
	// EncryptionKey is the effective access_key_encryption: the container
	// environment value recorded by the backup script, else config.json
	// (may be empty).
	EncryptionKey Secret
	// Files is the number of verified files.
	Files int
}

// OpenBackup refuses to continue unless dir contains a SHA256SUMS file whose
// entries all match, includes config.json and the database dump, and
// config.json is readable (MIG-2).
func OpenBackup(dir string) (*Backup, error) {
	if dir == "" {
		return nil, errors.New("--backup-dir is required")
	}
	sums, err := os.Open(filepath.Join(dir, "SHA256SUMS")) //nolint:gosec // operator-supplied backup path
	if err != nil {
		return nil, fmt.Errorf("backup check failed: SHA256SUMS not readable: %w", err)
	}
	defer func() { _ = sums.Close() }()

	seen := map[string]bool{}
	n := 0
	sc := bufio.NewScanner(sums)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		want, name, ok := strings.Cut(line, " ")
		name = strings.TrimPrefix(strings.TrimSpace(name), "*")
		if !ok || len(want) != sha256.Size*2 || name == "" {
			return nil, errors.New("backup check failed: malformed SHA256SUMS line")
		}
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return nil, errors.New("backup check failed: SHA256SUMS path escapes the backup directory")
		}
		got, err := hashFile(filepath.Join(dir, clean))
		if err != nil {
			return nil, fmt.Errorf("backup check failed: %s: %w", clean, err)
		}
		if got != strings.ToLower(want) {
			return nil, fmt.Errorf("backup check failed: checksum mismatch for %s", clean)
		}
		seen[clean] = true
		n++
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("backup check failed: %w", err)
	}
	for _, req := range []string{"config.json", "semaphore-db.sql.gz"} {
		if !seen[req] {
			return nil, fmt.Errorf("backup check failed: %s is not covered by SHA256SUMS", req)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "config.json")) //nolint:gosec // verified above
	if err != nil {
		return nil, fmt.Errorf("read config.json: %w", err)
	}
	var c struct {
		AccessKeyEncryption string `json:"access_key_encryption"`
	}
	if err := json.Unmarshal(cfg, &c); err != nil {
		return nil, errors.New("config.json is not valid JSON")
	}
	key := c.AccessKeyEncryption
	// Semaphore lets SEMAPHORE_ACCESS_KEY_ENCRYPTION override config.json.
	// The backup script records the container's effective value here.
	if seen[effectiveConfigFile] {
		eff, err := os.ReadFile(filepath.Join(dir, effectiveConfigFile)) //nolint:gosec // verified above
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", effectiveConfigFile, err)
		}
		var e struct {
			AccessKeyEncryption string `json:"access_key_encryption"`
		}
		if err := json.Unmarshal(eff, &e); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON", effectiveConfigFile)
		}
		if e.AccessKeyEncryption != "" {
			key = e.AccessKeyEncryption
		}
	}
	return &Backup{Dir: dir, EncryptionKey: NewSecret([]byte(key)), Files: n}, nil
}

// effectiveConfigFile holds settings taken from the Semaphore container
// environment, which take precedence over config.json.
const effectiveConfigFile = "semaphore-effective-config.json"

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is joined from a validated relative name
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
