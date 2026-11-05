package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const adminUsage = "usage: acp admin <create-breakglass --username NAME [--generate] | set-breakglass-password --username NAME [--generate] | reset-breakglass-totp --username NAME | rotate-master-key --new-key-file PATH | inventory import|list ... | project list|sync ... | template list|create ... | run start|show|list ...>"

// runAdmin executes operator tasks. Secrets (generated password, TOTP secret)
// go to stdout only; prompts and hints go to stderr.
func runAdmin(ctx context.Context, args []string, cfg config.Config, in *os.File, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(adminUsage)
	}
	if args[0] == "project" {
		return runAdminProject(ctx, args[1:], cfg, stdout, stderr)
	}
	if args[0] == "template" || args[0] == "run" {
		return runAdminRuns(ctx, args[0], args[1:], cfg, stdout, stderr)
	}
	if args[0] == "inventory" {
		return runAdminInventory(ctx, args[1:], cfg, stdout, stderr)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	username := fs.String("username", "", "break-glass username")
	generate := fs.Bool("generate", false, "generate a strong password instead of reading one")
	newKeyFile := fs.String("new-key-file", "", "file with the new master key (rotate-master-key)")
	switch args[0] {
	case "create-breakglass", "reset-breakglass-totp", "set-breakglass-password", "rotate-master-key":
	default:
		return fmt.Errorf("unknown admin command %q\n%s", args[0], adminUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cmd := args[0]
	if cmd == "rotate-master-key" {
		if *newKeyFile == "" {
			return errors.New("--new-key-file is required")
		}
		return rotateMasterKey(ctx, cfg, *newKeyFile, stdout, stderr)
	}
	if *username == "" {
		return errors.New("--username is required")
	}

	var password string
	generated := false
	if cmd == "create-breakglass" || cmd == "set-breakglass-password" {
		var err error
		if *generate {
			if password, err = auth.GeneratePassword(24); err != nil {
				return err
			}
			generated = true
		} else if password, err = readPassword(in, stderr); err != nil {
			return err
		}
		if len(password) < auth.MinPasswordLen {
			return auth.ErrWeakPassword
		}
	}

	sp, err := secrets.NewLocalProviderFromFile(cfg.MasterKeyFile, "ACP_MASTER_KEY")
	if err != nil {
		return fmt.Errorf("load master key: %w", err)
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	var enr admin.Enrollment
	switch cmd {
	case "create-breakglass":
		enr, err = admin.CreateBreakGlass(ctx, pool, sp, *username, password)
	case "reset-breakglass-totp":
		enr, err = admin.ResetBreakGlassTOTP(ctx, pool, sp, *username)
	case "set-breakglass-password":
		err = admin.SetBreakGlassPassword(ctx, pool, *username, password)
	}
	if err != nil {
		return err
	}
	if generated {
		_, _ = fmt.Fprintf(stdout, "password: %s\n", password)
		_, _ = fmt.Fprintln(stderr, "The generated password is shown only once. Store it safely.")
	}
	if cmd == "set-breakglass-password" {
		_, _ = fmt.Fprintln(stderr, "Password updated; lockout cleared.")
		return nil
	}
	_, _ = fmt.Fprintf(stdout, "totp_secret: %s\ntotp_uri: %s\n", enr.Secret, enr.URI)
	_, _ = fmt.Fprintln(stderr, "Enroll the TOTP secret in an authenticator app now; it is shown only once.")
	return nil
}

// rotateMasterKey re-wraps all stored data keys under the key in newKeyFile.
// Only counts are printed.
func rotateMasterKey(ctx context.Context, cfg config.Config, newKeyFile string, stdout, stderr io.Writer) error {
	oldKey, err := secrets.LoadMasterKey(cfg.MasterKeyFile, "ACP_MASTER_KEY")
	if err != nil {
		return fmt.Errorf("load current master key: %w", err)
	}
	defer clear(oldKey)
	newKey, err := secrets.LoadMasterKey(newKeyFile, "")
	if err != nil {
		return fmt.Errorf("load new master key: %w", err)
	}
	defer clear(newKey)
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	c, err := admin.RotateMasterKey(ctx, pool, oldKey, newKey)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "rotated_secrets: %d\nrotated_totp: %d\nalready_current: %d\n", c.Secrets, c.TOTP, c.AlreadyDone)
	_, _ = fmt.Fprintln(stderr, "Done. Now replace the configured master key with the new key and restart the app.")
	return nil
}

// readPassword reads without echo from a terminal (asking twice) or a single
// line from a pipe.
func readPassword(in *os.File, prompt io.Writer) (string, error) {
	fd := int(in.Fd()) //nolint:gosec // file descriptors fit in int
	if term.IsTerminal(fd) {
		_, _ = fmt.Fprint(prompt, "Password: ")
		a, err := term.ReadPassword(fd)
		_, _ = fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		_, _ = fmt.Fprint(prompt, "Repeat password: ")
		b, err := term.ReadPassword(fd)
		_, _ = fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		if string(a) != string(b) {
			return "", errors.New("passwords do not match")
		}
		return string(a), nil
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
