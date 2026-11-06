package semaphore

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
)

// row is one table row keyed by lower-case column name. Columns that do not
// exist in this Semaphore version are simply absent.
type row map[string]sql.NullString

func (r row) str(col string) string { return r[col].String }

func (r row) int(col string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(r[col].String), 10, 64)
	return v
}

// ptr returns nil for SQL NULL, empty and zero values (Semaphore uses NULL for
// "not set" foreign keys).
func (r row) ptr(col string) *int64 {
	v := r.int(col)
	if v == 0 {
		return nil
	}
	return &v
}

func (r row) bool(col string) bool {
	switch strings.ToLower(strings.TrimSpace(r[col].String)) {
	case "1", "true", "t", "yes":
		return true
	}
	return false
}

// tables holds the raw rows per table.
type tables struct {
	project, accessKey, repository, inventory, environment []row
	template, templateVault, schedule, user, projectUser   []row
	taskCount                                              int64
	warnings                                               []string
}

// buildSnapshot converts raw rows into typed objects. It is pure and
// tolerant: unknown columns are ignored and missing ones yield zero values.
func buildSnapshot(t tables) *Snapshot {
	s := &Snapshot{TaskCount: t.taskCount, Warnings: append([]string(nil), t.warnings...)}
	for _, r := range t.project {
		s.Projects = append(s.Projects, Project{ID: r.int("id"), Name: r.str("name")})
	}
	for _, r := range t.accessKey {
		s.Keys = append(s.Keys, Key{ID: r.int("id"), ProjectID: r.int("project_id"), Name: r.str("name"), Type: r.str("type"), Secret: r.str("secret")})
	}
	for _, r := range t.repository {
		s.Repositories = append(s.Repositories, Repository{
			ID: r.int("id"), ProjectID: r.int("project_id"), Name: r.str("name"),
			GitURL: r.str("git_url"), GitBranch: r.str("git_branch"), SSHKeyID: r.ptr("ssh_key_id"),
		})
	}
	for _, r := range t.inventory {
		s.Inventories = append(s.Inventories, Inventory{
			ID: r.int("id"), ProjectID: r.int("project_id"), Name: r.str("name"), Type: r.str("type"),
			Inventory: r.str("inventory"), SSHKeyID: r.ptr("ssh_key_id"), BecomeKeyID: r.ptr("become_key_id"),
			RepositoryID: r.ptr("repository_id"),
		})
	}
	for _, r := range t.environment {
		s.Environments = append(s.Environments, Environment{
			ID: r.int("id"), ProjectID: r.int("project_id"), Name: r.str("name"),
			JSON: r.str("json"), Env: r.str("env"), SecretValues: countSecretEntries(r),
		})
	}
	for _, r := range t.template {
		s.Templates = append(s.Templates, Template{
			ID: r.int("id"), ProjectID: r.int("project_id"), Name: r.str("name"), Playbook: r.str("playbook"),
			InventoryID: r.ptr("inventory_id"), RepositoryID: r.ptr("repository_id"), EnvironmentID: r.ptr("environment_id"),
			Arguments: r.str("arguments"), App: r.str("app"), SurveyVars: r.str("survey_vars"), GitBranch: r.str("git_branch"),
		})
	}
	for _, r := range t.templateVault {
		s.TemplateVault = append(s.TemplateVault, TemplateVault{
			TemplateID: r.int("template_id"), VaultKeyID: r.ptr("vault_key_id"), Name: r.str("name"), Type: r.str("type"),
		})
	}
	for _, r := range t.schedule {
		s.Schedules = append(s.Schedules, Schedule{
			ID: r.int("id"), ProjectID: r.int("project_id"), TemplateID: r.int("template_id"),
			Name: r.str("name"), CronFormat: r.str("cron_format"), Active: r.bool("active"),
		})
	}
	for _, r := range t.user {
		s.Users = append(s.Users, User{ID: r.int("id"), Username: r.str("username"), Name: r.str("name"), Email: r.str("email"), Admin: r.bool("admin")})
	}
	for _, r := range t.projectUser {
		s.ProjectUsers = append(s.ProjectUsers, ProjectUser{ProjectID: r.int("project_id"), UserID: r.int("user_id"), Role: r.str("role")})
	}
	return s
}

// countSecretEntries counts entries of the environment "secrets" JSON array
// without keeping any value. A legacy non-empty "password" column counts as 1.
func countSecretEntries(r row) int {
	n := 0
	if raw := strings.TrimSpace(r.str("secrets")); raw != "" && raw != "null" {
		var arr []json.RawMessage
		if json.Unmarshal([]byte(raw), &arr) == nil {
			n += len(arr)
		} else {
			n++
		}
	}
	if strings.TrimSpace(r.str("password")) != "" {
		n++
	}
	return n
}
