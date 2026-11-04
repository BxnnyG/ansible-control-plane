package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

type dirCheckouts struct{ dir string }

func (d dirCheckouts) WithCheckout(_ context.Context, _, _ uuid.UUID, fn func(*os.Root, string) error) error {
	root, err := os.OpenRoot(d.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fn(root, "0123456789abcdef0123456789abcdef01234567")
}

type fakeProjects []projects.Project

func (f fakeProjects) List(context.Context, uuid.UUID) ([]projects.Project, error) { return f, nil }

func TestImportInventoryCreateOrReimport(t *testing.T) {
	dsn := os.Getenv("ACP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ACP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(pool)
	slug := "w" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: slug, Name: "W"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	prj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/o/r.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hosts.ini"), []byte("[web]\nh1 ansible_host=192.0.2.1\nh2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.ini"), []byte("[db]\nh3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := &InventoryOps{
		Q:        q,
		Inv:      inventory.New(inventory.Options{DB: pool, Checkouts: dirCheckouts{dir}}),
		Projects: fakeProjects{{ID: prj.ID, Slug: "p", Name: "p"}},
	}
	in := InventoryImportInput{Workspace: slug, Project: "p", Path: "hosts.ini", Name: "Lab"}

	r, err := ops.ImportInventory(ctx, in)
	if err != nil || !r.Created || r.Hosts != 2 || r.Inventory.WriteBranch != "acp/lab" {
		t.Fatalf("create+import: %+v, %v", r, err)
	}
	// Project given by id re-imports into the same inventory.
	in2 := in
	in2.Project = prj.ID.String()
	r2, err := ops.ImportInventory(ctx, in2)
	if err != nil || r2.Created || r2.Inventory.ID != r.Inventory.ID {
		t.Fatalf("reimport: %+v, %v", r2, err)
	}
	list, err := ops.ListInventories(ctx, slug)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v, %v", list, err)
	}

	tests := []struct {
		name string
		mod  func(*InventoryImportInput)
		want error
	}{
		{"other path", func(i *InventoryImportInput) { i.Path = "other.ini" }, ErrInventoryMismatch},
		{"other mode", func(i *InventoryImportInput) { i.Mode = inventory.ModeDB }, ErrInventoryMismatch},
		{"unknown workspace", func(i *InventoryImportInput) { i.Workspace = "nope" }, ErrNoSuchWorkspace},
		{"unknown project", func(i *InventoryImportInput) { i.Project = "nope" }, ErrNoSuchProject},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			i := in
			tc.mod(&i)
			if _, err := ops.ImportInventory(ctx, i); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}
