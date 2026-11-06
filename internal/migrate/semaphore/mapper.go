package semaphore

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
)

// ACPProject is an existing ACP Git project of the target workspace.
type ACPProject struct {
	ID         string
	Slug       string
	URL        string
	DefaultRef string
}

// ACPInventory is an existing ACP inventory with its host identifiers (host
// names plus ansible_host values).
type ACPInventory struct {
	Slug  string
	Mode  string
	Hosts []string
}

// Target is the ACP side the mapper compares against.
type Target struct {
	Workspace   string
	Projects    []ACPProject
	Inventories []ACPInventory
	// Mappings holds existing migration_mappings: Kind+":"+SourceID -> ACP id.
	Mappings map[string]string
}

// KeyInfo is the printable summary of a decrypted key (no secret material).
type KeyInfo struct {
	Login       string
	Fingerprint string
	Unprotected bool
	Err         string // non-empty when decryption failed
}

// Options carries CLI input.
type Options struct {
	// InventoryMap maps a Semaphore inventory id to an ACP inventory slug.
	InventoryMap map[int64]string
}

// MappingKey builds the Mappings lookup key.
func MappingKey(kind string, sourceID int64) string {
	return kind + ":" + strconv.FormatInt(sourceID, 10)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a name to an ACP slug.
func Slugify(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "x"
	}
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

// Map converts a snapshot into a Report. It is pure: no I/O, no secrets.
// keys maps a Semaphore key id to its decryption result.
func Map(snap *Snapshot, keys map[int64]KeyInfo, tgt Target, opt Options) *Report {
	m := &mapper{snap: snap, keys: keys, tgt: tgt, opt: opt, rep: &Report{Workspace: tgt.Workspace}}
	m.run()
	return m.rep
}

type mapper struct {
	snap *Snapshot
	keys map[int64]KeyInfo
	tgt  Target
	opt  Options
	rep  *Report

	keyByID   map[int64]Key
	invByID   map[int64]Inventory
	envByID   map[int64]Environment
	repoByID  map[int64]Item // repository status
	repoPrj   map[int64]ACPProject
	invTarget map[int64]invResolution
	secretOK  map[int64]bool
	vaultKeys map[int64]bool
	usedKeys  map[int64]bool
	tmplOK    map[int64]bool
}

type invResolution struct {
	slug   string
	mapped bool // true: existing ACP inventory, false: archive
	err    string
}

func (m *mapper) run() {
	s := m.snap
	m.rep.Warnings = append(m.rep.Warnings, s.Warnings...)
	m.rep.TaskCount = s.TaskCount
	trimNames(s)
	m.keyByID, m.invByID, m.envByID = map[int64]Key{}, map[int64]Inventory{}, map[int64]Environment{}
	m.repoByID, m.repoPrj, m.invTarget = map[int64]Item{}, map[int64]ACPProject{}, map[int64]invResolution{}
	m.secretOK, m.vaultKeys, m.usedKeys, m.tmplOK = map[int64]bool{}, map[int64]bool{}, map[int64]bool{}, map[int64]bool{}
	for _, k := range s.Keys {
		m.keyByID[k.ID] = k
	}
	for _, i := range s.Inventories {
		m.invByID[i.ID] = i
	}
	for _, e := range s.Environments {
		m.envByID[e.ID] = e
	}
	if len(s.Projects) > 1 {
		m.rep.Warnings = append(m.rep.Warnings, fmt.Sprintf("%d Semaphore projects are merged into one workspace; names must stay unique", len(s.Projects)))
	}
	for _, tv := range s.TemplateVault {
		if tv.VaultKeyID != nil {
			m.vaultKeys[*tv.VaultKeyID] = true
		}
	}
	for _, i := range s.Inventories {
		for _, id := range []*int64{i.SSHKeyID, i.BecomeKeyID} {
			if id != nil {
				m.usedKeys[*id] = true
			}
		}
	}
	for _, r := range s.Repositories {
		if r.SSHKeyID != nil {
			m.usedKeys[*r.SSHKeyID] = true
		}
	}
	m.mapRepositories()
	m.mapKeys()
	m.mapInventories()
	m.mapEnvironments()
	m.mapTemplates()
	m.mapSchedules()
	m.mapUsers()
}

// trimNames strips surrounding whitespace from every source name so that
// ACP names and slugs never carry it.
func trimNames(s *Snapshot) {
	for i := range s.Projects {
		s.Projects[i].Name = strings.TrimSpace(s.Projects[i].Name)
	}
	for i := range s.Keys {
		s.Keys[i].Name = strings.TrimSpace(s.Keys[i].Name)
	}
	for i := range s.Repositories {
		s.Repositories[i].Name = strings.TrimSpace(s.Repositories[i].Name)
	}
	for i := range s.Inventories {
		s.Inventories[i].Name = strings.TrimSpace(s.Inventories[i].Name)
	}
	for i := range s.Environments {
		s.Environments[i].Name = strings.TrimSpace(s.Environments[i].Name)
	}
	for i := range s.Templates {
		s.Templates[i].Name = strings.TrimSpace(s.Templates[i].Name)
	}
	for i := range s.Schedules {
		s.Schedules[i].Name = strings.TrimSpace(s.Schedules[i].Name)
	}
}

func (m *mapper) existing(kind string, id int64) (string, bool) {
	v, ok := m.tgt.Mappings[MappingKey(kind, id)]
	return v, ok
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func (m *mapper) mapRepositories() {
	for _, r := range m.snap.Repositories {
		it := Item{Kind: KindRepository, SourceID: itoa(r.ID), Name: r.Name}
		norm, ok := NormalizeGitURL(r.GitURL)
		switch {
		case !ok:
			it.Status, it.Reason = StatusUnmappable, "local-path or unparsable repository URL"
		default:
			var found *ACPProject
			for i := range m.tgt.Projects {
				if n, ok := NormalizeGitURL(m.tgt.Projects[i].URL); ok && n == norm {
					found = &m.tgt.Projects[i]
					break
				}
			}
			if found == nil {
				it.Status, it.Reason = StatusUnmappable, "no ACP project with this Git URL; add it with `acp admin project` first"
			} else {
				it.Status, it.Target = StatusMatched, "project "+found.Slug
				m.repoPrj[r.ID] = *found
				if r.GitBranch != "" && found.DefaultRef != "" && r.GitBranch != found.DefaultRef {
					it.Notes = append(it.Notes, fmt.Sprintf("branch %q differs from ACP default ref %q", r.GitBranch, found.DefaultRef))
				}
			}
		}
		if r.SSHKeyID != nil {
			it.Notes = append(it.Notes, "SSH key is not migrated; the ACP project has its own Git credential")
		}
		m.repoByID[r.ID] = it
		m.rep.Repositories = append(m.rep.Repositories, it)
	}
}

func (m *mapper) mapKeys() {
	for _, k := range m.snap.Keys {
		it := Item{Kind: KindSecret, SourceID: itoa(k.ID), Name: k.Name}
		if k.Type == "none" || k.Type == "" {
			it.Status, it.Reason = StatusSkipped, "key type none carries no secret"
			m.rep.Secrets = append(m.rep.Secrets, it)
			continue
		}
		info := m.keys[k.ID]
		name := "semaphore: " + k.Name
		it.Target = name
		var typ string
		switch k.Type {
		case "ssh":
			typ = "ssh_key"
		case "login_password":
			typ = "password"
			if m.vaultKeys[k.ID] {
				typ = "vault_password"
			}
		default:
			it.Status, it.Reason = StatusUnmappable, fmt.Sprintf("unsupported key type %q", k.Type)
			m.rep.Secrets = append(m.rep.Secrets, it)
			continue
		}
		it.Notes = append(it.Notes, "type "+typ)
		if info.Err != "" {
			it.Status, it.Reason = StatusUnmappable, "cannot decrypt: "+info.Err
			m.rep.Secrets = append(m.rep.Secrets, it)
			continue
		}
		if info.Login != "" {
			it.Notes = append(it.Notes, "login "+info.Login)
		}
		if info.Fingerprint != "" {
			it.Notes = append(it.Notes, "fingerprint "+info.Fingerprint)
		}
		if info.Unprotected {
			it.Notes = append(it.Notes, "passphrase removed; stored unencrypted inside the ACP envelope")
		}
		if !m.usedKeys[k.ID] && !m.vaultKeys[k.ID] {
			it.Notes = append(it.Notes, "not referenced by any inventory or vault entry")
		}
		m.secretOK[k.ID] = true
		if _, ok := m.existing(KindSecret, k.ID); ok {
			it.Status = StatusExists
		} else {
			it.Status = StatusNew
			m.rep.PlanSecrets = append(m.rep.PlanSecrets, PlannedSecret{SourceKeyID: k.ID, Name: name, Type: typ, Login: info.Login, Fingerprint: info.Fingerprint})
		}
		if typ == "vault_password" {
			label := vaultLabel(k.Name)
			if _, ok := m.existing(KindVault, k.ID); !ok {
				m.rep.PlanVaults = append(m.rep.PlanVaults, PlannedVault{SourceKeyID: k.ID, Label: label})
			}
			it.Notes = append(it.Notes, "vault identity "+label)
		}
		m.rep.Secrets = append(m.rep.Secrets, it)
	}
}

// vaultLabel derives a valid vault-id label (letters, digits, _ . -; max 63).
func vaultLabel(name string) string {
	l := "semaphore-" + Slugify(name)
	if len(l) > 63 {
		l = strings.TrimRight(l[:63], "-")
	}
	return l
}

// hostIDs returns the host names and ansible_host values of static inventory
// content, plus the detected format.
func hostIDs(content string) (ids []string, format string, err error) {
	t := strings.TrimSpace(content)
	var doc *inventory.Doc
	if strings.HasPrefix(t, "---") || strings.HasPrefix(t, "all:") || strings.Contains(t, "\nhosts:") || strings.HasPrefix(t, "{") {
		format = "yaml"
		doc, err = inventory.ParseYAML([]byte(content))
	} else {
		format = "ini"
		doc, err = inventory.ParseINI([]byte(content))
	}
	if err != nil {
		return nil, format, err
	}
	for _, h := range doc.Hosts {
		ids = append(ids, h.Name)
		if v, ok := h.Vars["ansible_host"].(string); ok && v != "" {
			ids = append(ids, v)
		}
	}
	return ids, format, nil
}

func (m *mapper) mapInventories() {
	for _, inv := range m.snap.Inventories {
		it := Item{Kind: KindInventory, SourceID: itoa(inv.ID), Name: inv.Name}
		res := invResolution{}
		isStatic := inv.Type == "static" || inv.Type == "static-yaml" || inv.Type == ""
		var hosts []string
		format := "ini"
		if isStatic {
			h, f, err := hostIDs(inv.Inventory)
			if err != nil {
				m.rep.Warnings = append(m.rep.Warnings, fmt.Sprintf("inventory %q: content could not be parsed (%v); it is archived only if the parser accepts it", inv.Name, err))
			}
			hosts, format = h, f
		}
		// Suggestion by host overlap.
		if len(hosts) > 0 {
			if sg, ok := suggest(inv, hosts, m.tgt.Inventories); ok {
				m.rep.Suggestions = append(m.rep.Suggestions, sg)
			}
		}
		arch := "semaphore-" + Slugify(inv.Name)
		slug, mapped := m.opt.InventoryMap[inv.ID]
		archived := ""
		if isStatic {
			// Every static inventory is archived losslessly, also when
			// templates are re-pointed to a Git inventory.
			if _, ok := m.existing(KindInventory, inv.ID); ok {
				archived = "archive " + arch + " exists"
			} else {
				archived = "archive " + arch + " will be created"
				m.rep.PlanInventories = append(m.rep.PlanInventories, PlannedInventory{SourceID: inv.ID, Name: "semaphore-" + inv.Name, Slug: arch, Format: format})
			}
		}
		switch {
		case mapped:
			if !m.hasInventory(slug) {
				res.err = fmt.Sprintf("--inventory-map target %q does not exist in ACP", slug)
				it.Status, it.Reason = StatusUnmappable, res.err
			} else {
				res = invResolution{slug: slug, mapped: true}
				it.Status, it.Target = StatusMatched, "inventory "+slug+" (--inventory-map)"
			}
			if archived != "" {
				it.Notes = append(it.Notes, archived)
			}
		case isStatic:
			res = invResolution{slug: arch}
			if _, ok := m.existing(KindInventory, inv.ID); ok {
				it.Status = StatusExists
			} else {
				it.Status = StatusNew
			}
			it.Target = "inventory " + arch + " (archive)"
			it.Notes = append(it.Notes, fmt.Sprintf("%d host identifiers; use --inventory-map %d=<acp-inventory> to re-point templates to a Git inventory", len(hosts), inv.ID))
		default:
			res.err = fmt.Sprintf("inventory type %q lives in the repository; map it with --inventory-map %d=<acp-inventory>", inv.Type, inv.ID)
			it.Status, it.Reason = StatusUnmappable, res.err
		}
		m.invTarget[inv.ID] = res
		m.rep.Inventories = append(m.rep.Inventories, it)
	}
}

func (m *mapper) hasInventory(slug string) bool {
	for _, i := range m.tgt.Inventories {
		if i.Slug == slug {
			return true
		}
	}
	return false
}

// suggest returns the ACP inventory with the highest host overlap (>= 50%).
func suggest(src Inventory, hosts []string, acp []ACPInventory) (Suggestion, bool) {
	set := map[string]bool{}
	for _, h := range hosts {
		set[strings.ToLower(h)] = true
	}
	best := Suggestion{SourceID: src.ID, SourceName: src.Name}
	found := false
	for _, a := range acp {
		have := map[string]bool{}
		for _, h := range a.Hosts {
			have[strings.ToLower(h)] = true
		}
		hit := 0
		for h := range set {
			if have[h] {
				hit++
			}
		}
		ov := float64(hit) / float64(len(set))
		if ov >= 0.5 && ov > best.Overlap {
			best.Slug, best.Overlap, found = a.Slug, ov, true
		}
	}
	return best, found
}

func (m *mapper) mapEnvironments() {
	for _, e := range m.snap.Environments {
		it := Item{Kind: "environment", SourceID: itoa(e.ID), Name: e.Name, Status: StatusMatched, Target: "template extra_vars / env_vars"}
		if _, _, err := parseEnv(e); err != nil {
			it.Status, it.Reason = StatusUnmappable, err.Error()
		}
		if e.SecretValues > 0 {
			it.Notes = append(it.Notes, fmt.Sprintf("%d secret value(s) are not migrated; recreate them manually", e.SecretValues))
		}
		m.rep.Environments = append(m.rep.Environments, it)
	}
}

func parseEnv(e Environment) (map[string]any, map[string]string, error) {
	extra := map[string]any{}
	if t := strings.TrimSpace(e.JSON); t != "" && t != "null" {
		if err := json.Unmarshal([]byte(t), &extra); err != nil {
			return nil, nil, fmt.Errorf("environment JSON is not an object")
		}
	}
	env := map[string]string{}
	if t := strings.TrimSpace(e.Env); t != "" && t != "null" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(t), &raw); err != nil {
			return nil, nil, fmt.Errorf("environment ENV is not an object")
		}
		for k, v := range raw {
			if s, ok := v.(string); ok {
				env[k] = s
			} else {
				b, _ := json.Marshal(v)
				env[k] = string(b)
			}
		}
	}
	return extra, env, nil
}

func (m *mapper) keyUsable(id *int64) (ok bool, reason string) {
	if id == nil {
		return true, ""
	}
	k, found := m.keyByID[*id]
	if !found {
		return false, fmt.Sprintf("key %d not found", *id)
	}
	if k.Type == "none" || k.Type == "" {
		return true, ""
	}
	if !m.secretOK[*id] {
		return false, fmt.Sprintf("key %q cannot be migrated", k.Name)
	}
	return true, ""
}

func (m *mapper) mapTemplates() {
	for _, t := range m.snap.Templates {
		it := Item{Kind: KindTemplate, SourceID: itoa(t.ID), Name: t.Name}
		fail := func(reason string) {
			it.Status, it.Reason = StatusUnmappable, reason
			m.rep.Templates = append(m.rep.Templates, it)
		}
		if t.App != "" && t.App != "ansible" {
			fail(fmt.Sprintf("app %q is not supported (ansible only)", t.App))
			continue
		}
		if t.RepositoryID == nil {
			fail("template has no repository")
			continue
		}
		prj, ok := m.repoPrj[*t.RepositoryID]
		if !ok {
			fail("repository cannot be mapped to an ACP project")
			continue
		}
		if t.InventoryID == nil {
			fail("template has no inventory")
			continue
		}
		res, ok := m.invTarget[*t.InventoryID]
		if !ok || res.err != "" {
			fail("inventory cannot be mapped: " + res.err)
			continue
		}
		inv := m.invByID[*t.InventoryID]
		pt := PlannedTemplate{SourceID: t.ID, Name: t.Name, Playbook: t.Playbook, ProjectID: prj.ID, Inventory: res.slug, InventoryM: res.mapped, InventorySrc: *t.InventoryID}
		bad := ""
		if ok, why := m.keyUsable(inv.SSHKeyID); !ok {
			bad = why
		} else if ok, why := m.keyUsable(inv.BecomeKeyID); !ok {
			bad = why
		} else if id := realKey(m.keyByID, inv.SSHKeyID); id != nil && m.keyByID[*id].Type != "ssh" {
			bad = fmt.Sprintf("SSH key %q is not an ssh key", m.keyByID[*id].Name)
		} else if id := realKey(m.keyByID, inv.BecomeKeyID); id != nil && (m.keyByID[*id].Type != "login_password" || m.vaultKeys[*id]) {
			bad = fmt.Sprintf("become key %q must be a plain login_password key (not an ssh or vault key)", m.keyByID[*id].Name)
		}
		if bad != "" {
			fail(bad)
			continue
		}
		pt.SSHKeyID, pt.BecomeID = realKey(m.keyByID, inv.SSHKeyID), realKey(m.keyByID, inv.BecomeKeyID)
		if pt.SSHKeyID != nil {
			if login := m.keys[*pt.SSHKeyID].Login; login != "" {
				pt.ExtraVars = map[string]any{"ansible_user": login}
				it.Notes = append(it.Notes, "extra var ansible_user="+login+" (from SSH key login)")
			}
		}
		if t.EnvironmentID != nil {
			env, found := m.envByID[*t.EnvironmentID]
			if !found {
				fail(fmt.Sprintf("environment %d not found", *t.EnvironmentID))
				continue
			}
			extra, envv, err := parseEnv(env)
			if err != nil {
				fail("environment: " + err.Error())
				continue
			}
			if pt.ExtraVars == nil {
				pt.ExtraVars = map[string]any{}
			}
			for k, v := range extra {
				if k == "ansible_user" && pt.ExtraVars[k] != nil {
					it.Notes = append(it.Notes, "environment sets ansible_user and overrides the SSH key login")
				}
				pt.ExtraVars[k] = v
			}
			if len(envv) > 0 {
				pt.EnvVars = envv
			}
			if env.SecretValues > 0 {
				it.Notes = append(it.Notes, fmt.Sprintf("environment %q has %d secret value(s) that are not migrated", env.Name, env.SecretValues))
			}
		}
		a := ParseArguments(t.Arguments)
		pt.Limit, pt.Tags, pt.SkipTags, pt.Verbosity, pt.Check, pt.Diff = a.Limit, a.Tags, a.SkipTags, a.Verbosity, a.Check, a.Diff
		if len(a.Unmapped) > 0 {
			it.Notes = append(it.Notes, "arguments not migrated: "+strings.Join(uniq(a.Unmapped), ", "))
		}
		for _, tv := range m.snap.TemplateVault {
			if tv.TemplateID != t.ID {
				continue
			}
			if tv.VaultKeyID == nil || !m.secretOK[*tv.VaultKeyID] {
				it.Notes = append(it.Notes, "a vault entry has no migratable key and is skipped")
				continue
			}
			pt.VaultKeys = append(pt.VaultKeys, *tv.VaultKeyID)
		}
		if len(pt.VaultKeys) > 0 {
			it.Notes = append(it.Notes, fmt.Sprintf("%d vault identity(ies)", len(pt.VaultKeys)))
		}
		if sv := strings.TrimSpace(t.SurveyVars); sv != "" && sv != "[]" && sv != "null" {
			fields, note := mapSurvey(sv)
			pt.Survey = fields
			if note != "" {
				it.Notes = append(it.Notes, note)
			}
		}
		if t.GitBranch != "" && prj.DefaultRef != "" && t.GitBranch != prj.DefaultRef {
			it.Notes = append(it.Notes, fmt.Sprintf("template branch %q differs from project default ref %q", t.GitBranch, prj.DefaultRef))
		}
		it.Target = "template in project " + prj.Slug + ", inventory " + res.slug
		m.tmplOK[t.ID] = true
		if _, ok := m.existing(KindTemplate, t.ID); ok {
			it.Status = StatusExists
		} else {
			it.Status = StatusNew
			m.rep.PlanTemplates = append(m.rep.PlanTemplates, pt)
		}
		m.rep.Templates = append(m.rep.Templates, it)
	}
}

func realKey(keys map[int64]Key, id *int64) *int64 {
	if id == nil {
		return nil
	}
	if k := keys[*id]; k.Type == "none" || k.Type == "" {
		return nil
	}
	return id
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// mapSurvey converts Semaphore survey_vars to ACP survey fields.
func mapSurvey(raw string) ([]scan.Field, string) {
	var in []struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Type        string `json:"type"`
		Required    bool   `json:"required"`
		Values      []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, "survey variables could not be parsed and are not migrated"
	}
	var out []scan.Field
	var notes []string
	for _, v := range in {
		if v.Name == "" {
			continue
		}
		f := scan.Field{Name: v.Name, Required: v.Required, Description: v.Description, Type: scan.TypeStr}
		if f.Description == "" {
			f.Description = v.Title
		}
		switch v.Type {
		case "int":
			f.Type = scan.TypeInt
		case "secret":
			f.Secret = true
		case "enum":
			for _, c := range v.Values {
				f.Choices = append(f.Choices, c.Value)
			}
		case "", "string":
		default:
			notes = append(notes, fmt.Sprintf("survey type %q of %q treated as string", v.Type, v.Name))
		}
		out = append(out, f)
	}
	note := fmt.Sprintf("%d survey field(s)", len(out))
	if len(notes) > 0 {
		note += "; " + strings.Join(notes, "; ")
	}
	return out, note
}

var cronRe = regexp.MustCompile(`^@(yearly|annually|monthly|weekly|daily|midnight|hourly)$`)

func (m *mapper) mapSchedules() {
	for _, s := range m.snap.Schedules {
		it := Item{Kind: KindSchedule, SourceID: itoa(s.ID), Name: s.Name}
		if it.Name == "" {
			it.Name = "template " + itoa(s.TemplateID)
		}
		cron := strings.TrimSpace(s.CronFormat)
		switch {
		case !m.tmplOK[s.TemplateID]:
			it.Status, it.Reason = StatusUnmappable, "its template cannot be migrated"
		case cron == "" || (len(strings.Fields(cron)) != 5 && !cronRe.MatchString(cron)):
			it.Status, it.Reason = StatusUnmappable, "unsupported cron expression (need 5 fields)"
		default:
			it.Target = "schedule " + cron + " (disabled)"
			if s.Active {
				it.Notes = append(it.Notes, "active in Semaphore; created disabled, enable it after cutover")
			}
			if _, ok := m.existing(KindSchedule, s.ID); ok {
				it.Status = StatusExists
			} else {
				it.Status = StatusNew
				m.rep.PlanSchedules = append(m.rep.PlanSchedules, PlannedSchedule{SourceID: s.ID, TemplateID: s.TemplateID, Name: s.Name, Cron: cron})
			}
		}
		m.rep.Schedules = append(m.rep.Schedules, it)
	}
}

func (m *mapper) mapUsers() {
	roles := map[int64][]string{}
	for _, pu := range m.snap.ProjectUsers {
		roles[pu.UserID] = append(roles[pu.UserID], pu.Role)
	}
	for _, u := range m.snap.Users {
		name := u.Username
		if name == "" {
			name = u.Name
		}
		rs := uniq(roles[u.ID])
		m.rep.Users = append(m.rep.Users, UserSummary{Username: name, Admin: u.Admin, Roles: strings.Join(rs, ",")})
	}
}
