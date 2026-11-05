package workerapi

import (
	"archive/tar"
	"context"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultids"
)

const (
	testKey      = "-----BEGIN OPENSSH " + "PRIVATE KEY-----\nQUJDREVGR0hJSktMTU5PUA==\n-----END OPENSSH " + "PRIVATE KEY-----\n"
	testBecome   = "become-pw-123"
	testVault    = "vault-pw-456"
	testSurveyPw = "survey-secret-789"
)

type fakeTrees struct{ rev string }

func (f *fakeTrees) WriteTree(_ context.Context, _, _ uuid.UUID, rev, prefix string, w io.Writer) error {
	f.rev = rev
	tw := tar.NewWriter(w)
	body := []byte("- hosts: all\n  tasks: []\n")
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "x"}}); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: prefix + "site.yml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(body); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	// Like git archive: pad with zero blocks past the tar end marker.
	_, err := w.Write(make([]byte, 10240))
	return err
}

type fakeInventories struct{}

func (fakeInventories) LoadDoc(context.Context, uuid.UUID, uuid.UUID) (*inventory.Doc, error) {
	return &inventory.Doc{
		Hosts: []*inventory.Host{{Name: "web1.example.test", Vars: map[string]any{
			"ansible_user": "deploy", "token": map[string]any{inventory.VaultKey: "$ANSIBLE_VAULT;1.1;AES256\n6162"},
		}}},
		Groups: []*inventory.Group{{Name: "web", Hosts: []string{"web1.example.test"}}},
	}, nil
}

type fakeSecrets struct {
	byID map[uuid.UUID]secrets.Resolved
}

func (f fakeSecrets) Resolve(_ context.Context, _, id uuid.UUID) (secrets.Resolved, error) {
	r, ok := f.byID[id]
	if !ok {
		return secrets.Resolved{}, secrets.ErrNotFound
	}
	return secrets.Resolved{Type: r.Type, Name: r.Name, Value: secrets.NewValue(append([]byte(nil), r.Value.Reveal()...))}, nil
}

type fakeVault struct{ id uuid.UUID }

func (f fakeVault) Get(_ context.Context, _, id uuid.UUID) (vaultids.Identity, error) {
	if id != f.id {
		return vaultids.Identity{}, vaultids.ErrNotFound
	}
	return vaultids.Identity{ID: id, Label: "prod", Provider: vaultids.ProviderSecret}, nil
}

func (fakeVault) Password(_ context.Context, _ uuid.UUID, label string) (vaultids.Password, error) {
	return vaultids.NewPassword(label, testVault+"\n"), nil
}

func testProvider(t *testing.T) secrets.Provider {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*5 + 3)
	}
	p, err := secrets.NewLocalProvider(key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type fixtureIDs struct{ ssh, become, vault uuid.UUID }

func testBuilder(t *testing.T, ids fixtureIDs) (*Builder, *fakeTrees) {
	t.Helper()
	trees := &fakeTrees{}
	return &Builder{
		Trees: trees, Inventories: fakeInventories{}, Provider: testProvider(t), Vault: fakeVault{id: ids.vault},
		Secrets: fakeSecrets{byID: map[uuid.UUID]secrets.Resolved{
			ids.ssh:    {Type: secrets.TypeSSHKey, Value: secrets.NewValue([]byte(testKey))},
			ids.become: {Type: secrets.TypePassword, Value: secrets.NewValue([]byte(testBecome))},
		}},
	}, trees
}

func readTarGz(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := gzipReader(b)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		out[h.Name] = data
	}
}
