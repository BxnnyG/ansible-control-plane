// Package semaphore reads a Semaphore UI installation (MySQL dialect, v2.x),
// decrypts its key store and maps the content to ACP objects (ADR-0017).
//
// The package only ever reads. The source is a restored backup copy, never the
// live Semaphore database.
package semaphore

import "context"

// Project is a Semaphore project.
type Project struct {
	ID   int64
	Name string
}

// Key is an access_key row. Secret holds the still encrypted column value.
type Key struct {
	ID        int64
	ProjectID int64
	Name      string
	Type      string // ssh, login_password, none, ...
	Secret    string
}

// Repository is a project__repository row.
type Repository struct {
	ID        int64
	ProjectID int64
	Name      string
	GitURL    string
	GitBranch string
	SSHKeyID  *int64
}

// Inventory is a project__inventory row.
type Inventory struct {
	ID           int64
	ProjectID    int64
	Name         string
	Type         string // static, static-yaml, file, ...
	Inventory    string // inline content (static) or path (file)
	SSHKeyID     *int64
	BecomeKeyID  *int64
	RepositoryID *int64
}

// Environment is a project__environment row.
type Environment struct {
	ID           int64
	ProjectID    int64
	Name         string
	JSON         string // extra vars
	Env          string // process environment
	SecretValues int    // number of secret entries (values are never read)
}

// Template is a project__template row.
type Template struct {
	ID            int64
	ProjectID     int64
	Name          string
	Playbook      string
	InventoryID   *int64
	RepositoryID  *int64
	EnvironmentID *int64
	Arguments     string
	App           string
	SurveyVars    string
	GitBranch     string
}

// TemplateVault links a template to a vault key.
type TemplateVault struct {
	TemplateID int64
	VaultKeyID *int64
	Name       string
	Type       string
}

// Schedule is a project__schedule row.
type Schedule struct {
	ID         int64
	ProjectID  int64
	TemplateID int64
	Name       string
	CronFormat string
	Active     bool
}

// User is a Semaphore account (reported only).
type User struct {
	ID       int64
	Username string
	Name     string
	Email    string
	Admin    bool
}

// ProjectUser is a project membership (reported only).
type ProjectUser struct {
	ProjectID int64
	UserID    int64
	Role      string
}

// Snapshot is the complete, read-only view of one Semaphore installation.
type Snapshot struct {
	Projects      []Project
	Keys          []Key
	Repositories  []Repository
	Inventories   []Inventory
	Environments  []Environment
	Templates     []Template
	TemplateVault []TemplateVault
	Schedules     []Schedule
	Users         []User
	ProjectUsers  []ProjectUser
	TaskCount     int64
	// Warnings lists tables or columns that were missing or unreadable.
	Warnings []string
}

// Source provides a Snapshot. The MySQL reader is the only implementation;
// other Semaphore dialects are out of scope for v1.0.
type Source interface {
	Load(ctx context.Context) (*Snapshot, error)
}
