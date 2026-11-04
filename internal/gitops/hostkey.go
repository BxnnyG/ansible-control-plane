package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// HostKey is one scanned SSH host key.
type HostKey struct {
	Type        string // e.g. ssh-ed25519
	KnownHosts  string // line in known_hosts format
	Fingerprint string // SHA256:... as shown by ssh-keygen -l
}

// ScanHostKey fetches host keys with ssh-keyscan for trust-on-first-use confirmation.
// The result must be confirmed by a user before it is stored as known_hosts.
func (c *Client) ScanHostKey(ctx context.Context, host string, port int) ([]HostKey, error) {
	if !hostRe.MatchString(host) || port < 0 || port > 65535 {
		return nil, invalid("host or port")
	}
	if port == 0 {
		port = 22
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh-keyscan", "-T", "10", "-p", strconv.Itoa(port), host) //nolint:gosec // host validated by hostRe
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ssh-keyscan: %w: %s", err, strings.TrimSpace(redact(errb.String())))
	}
	keys := parseKeyscan(out.String())
	if len(keys) == 0 {
		return nil, errors.New("ssh-keyscan: no host keys returned")
	}
	return keys, nil
}

func parseKeyscan(s string) []HostKey {
	var keys []HostKey
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(f[2])
		if err != nil {
			continue
		}
		sum := sha256.Sum256(raw)
		keys = append(keys, HostKey{
			Type:        f[1],
			KnownHosts:  strings.Join(f[:3], " "),
			Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]),
		})
	}
	return keys
}
