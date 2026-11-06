package semaphore

import (
	"database/sql"
	"testing"
)

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func TestBuildSnapshot(t *testing.T) {
	tb := tables{
		project:   []row{{"id": ns("1"), "name": ns("Infra")}},
		accessKey: []row{{"id": ns("3"), "name": ns("k"), "type": ns("ssh"), "project_id": ns("1"), "secret": ns("abc")}},
		inventory: []row{{"id": ns("5"), "name": ns("inv"), "type": ns("static"), "ssh_key_id": ns("3"), "become_key_id": {}}},
		environment: []row{
			{"id": ns("2"), "name": ns("e"), "json": ns(`{"a":1}`), "secrets": ns(`[{"name":"x"},{"name":"y"}]`)},
			{"id": ns("3"), "name": ns("legacy"), "password": ns("zzz")},
		},
		schedule:  []row{{"id": ns("9"), "template_id": ns("7"), "cron_format": ns("0 3 * * *"), "active": ns("1")}},
		taskCount: 42,
		warnings:  []string{"w"},
	}
	s := buildSnapshot(tb)
	if len(s.Projects) != 1 || s.Projects[0].Name != "Infra" {
		t.Fatalf("projects: %+v", s.Projects)
	}
	if s.Keys[0].Type != "ssh" || s.Keys[0].ProjectID != 1 {
		t.Fatalf("keys: %+v", s.Keys)
	}
	inv := s.Inventories[0]
	if inv.SSHKeyID == nil || *inv.SSHKeyID != 3 || inv.BecomeKeyID != nil || inv.RepositoryID != nil {
		t.Fatalf("inventory fks: %+v", inv)
	}
	if s.Environments[0].SecretValues != 2 || s.Environments[1].SecretValues != 1 {
		t.Fatalf("env secrets: %+v", s.Environments)
	}
	if !s.Schedules[0].Active || s.Schedules[0].TemplateID != 7 {
		t.Fatalf("schedule: %+v", s.Schedules[0])
	}
	if s.TaskCount != 42 || len(s.Warnings) != 1 {
		t.Fatalf("meta: %d %v", s.TaskCount, s.Warnings)
	}
}
