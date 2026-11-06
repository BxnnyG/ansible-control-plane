// Package fleet derives unified hosts and conflict hints from connector
// observations (ADR-0010, ADR-0018). Merge is a pure function; the Store
// persists its result with stable host ids.
package fleet

import (
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

// DisappearedAfter is the age of the newest observation after which a host
// counts as disappeared.
const DisappearedAfter = 24 * time.Hour

// Key kinds (host_keys.kind), ordered from strongest to weakest.
const (
	KeyMAC  = "mac"
	KeyName = "name"
	KeyIP   = "ip"
)

// Conflict types (host_conflicts.type).
const (
	ConflictIPMAC             = "ip_conflict"
	ConflictDuplicateHostname = "duplicate_hostname"
	ConflictDisappeared       = "host_disappeared"
)

// Observation is one stored observation together with its connector.
type Observation struct {
	ConnectorID   uuid.UUID
	ConnectorName string
	ConnectorType string
	ExternalKey   string
	Kind          string
	Fields        connectors.Fields
	FirstSeen     time.Time
	LastSeen      time.Time
}

// Priority ranks connectors; a higher value wins a field. A per-connector
// value overrides the per-type value; unknown types get DefaultTypePriority.
type Priority struct {
	ByType      map[string]int
	ByConnector map[uuid.UUID]int
}

// DefaultTypePriority is used for connector types without an entry
// (monitoring connectors rank last: NetBox > Proxmox > OPNsense > monitoring).
const DefaultTypePriority = 100

// DefaultPriority returns the default source ranking of ADR-0010.
func DefaultPriority() Priority {
	return Priority{ByType: map[string]int{"netbox": 400, "proxmox": 300, "opnsense": 200}}
}

// Of returns the priority of an observation's connector.
func (p Priority) Of(o Observation) int {
	if v, ok := p.ByConnector[o.ConnectorID]; ok {
		return v
	}
	if v, ok := p.ByType[o.ConnectorType]; ok {
		return v
	}
	return DefaultTypePriority
}

// Key is one identity key of a host.
type Key struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// String renders "kind:value".
func (k Key) String() string { return k.Kind + ":" + k.Value }

// Source names the connector a field value came from.
type Source struct {
	ConnectorID   uuid.UUID `json:"connector_id"`
	ConnectorName string    `json:"connector"`
	ConnectorType string    `json:"type"`
}

// Fields are the merged attributes of a host. Sources maps a field name
// (hostname, fqdn, kind, status, interface, notes, macs, ips, tags) to the
// connectors that contributed: the winning connector for scalar fields, all
// contributors (highest priority first) for list fields.
type Fields struct {
	Kind      string   `json:"kind,omitempty"`
	Hostname  string   `json:"hostname,omitempty"`
	FQDN      string   `json:"fqdn,omitempty"`
	MACs      []string `json:"macs,omitempty"`
	IPs       []string `json:"ips,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Notes     string   `json:"notes,omitempty"`
	Status    string   `json:"status,omitempty"`
	Interface string   `json:"interface,omitempty"`
	// Attrs are the classification attributes of the contributing
	// observations; the highest-priority source wins per key.
	Attrs   map[string]string   `json:"attrs,omitempty"`
	Sources map[string][]Source `json:"sources,omitempty"`
	// NetSeen is the newest last_seen of the lease, ARP and NDP observations;
	// Power is the status reported by the highest-priority VM, LXC or device
	// observation. Both feed the online status (see OnlineOf).
	NetSeen *time.Time `json:"net_seen,omitempty"`
	Power   string     `json:"power,omitempty"`
}

// MemberRef identifies one observation (connector and external key).
type MemberRef struct {
	ConnectorID uuid.UUID
	ExternalKey string
}

// Host is one merged host.
type Host struct {
	Name         string // display name: hostname, else FQDN
	Fields       Fields
	Keys         []Key // sorted: mac, name, ip
	Observations int
	// Members identifies the observations the host was built from.
	Members   []MemberRef
	FirstSeen time.Time
	LastSeen  time.Time
}

// Strongest returns the strongest key (MAC > name > IP), or false for a host
// without keys.
func (h Host) Strongest() (Key, bool) {
	if len(h.Keys) == 0 {
		return Key{}, false
	}
	return h.Keys[0], true
}

// Conflict is a contradiction hint. Hosts index into the merged host slice.
type Conflict struct {
	Type        string
	Fingerprint string // type + sorted keys; stable across re-merges
	Hosts       []int
	Details     map[string]any
}

// Result is the outcome of Merge.
type Result struct {
	Hosts     []Host
	Conflicts []Conflict
}

// normName lower-cases a host name and strips a trailing dot.
func normName(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

func normMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 17 || strings.Count(s, ":") != 5 {
		return ""
	}
	return s
}

// normIP returns the canonical address string, or "" for invalid addresses
// and addresses that cannot identify a host (loopback, link-local,
// unspecified, multicast).
func normIP(s string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsMulticast() {
		return ""
	}
	return a.String()
}

// obs is an observation with normalized identity keys.
type obs struct {
	Observation
	prio  int
	macs  []string
	names []string
	ips   []string
}

type dsu struct{ parent []int }

func newDSU(n int) *dsu {
	d := &dsu{parent: make([]int, n)}
	for i := range d.parent {
		d.parent[i] = i
	}
	return d
}

func (d *dsu) find(i int) int {
	for d.parent[i] != i {
		d.parent[i] = d.parent[d.parent[i]]
		i = d.parent[i]
	}
	return i
}

// union joins two sets; the smaller root index wins so results are stable.
func (d *dsu) union(a, b int) int {
	a, b = d.find(a), d.find(b)
	if a == b {
		return a
	}
	if b < a {
		a, b = b, a
	}
	d.parent[b] = a
	return a
}

// Merge derives hosts and conflicts from observations. The result does not
// depend on the order of the input.
func Merge(in []Observation, prio Priority, now time.Time) Result {
	all := make([]obs, 0, len(in))
	for _, o := range in {
		all = append(all, obs{Observation: o, prio: prio.Of(o)})
	}
	// Canonical order: highest priority first, then newest, then by identity.
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.prio != b.prio:
			return a.prio > b.prio
		case !a.LastSeen.Equal(b.LastSeen):
			return a.LastSeen.After(b.LastSeen)
		case a.ConnectorName != b.ConnectorName:
			return a.ConnectorName < b.ConnectorName
		case a.ConnectorID != b.ConnectorID:
			return a.ConnectorID.String() < b.ConnectorID.String()
		}
		return a.ExternalKey < b.ExternalKey
	})
	normalize(all)

	d := newDSU(len(all))
	macs := make([]map[string]bool, len(all)) // MAC set per root
	for i, o := range all {
		macs[i] = map[string]bool{}
		for _, m := range o.macs {
			macs[i][m] = true
		}
	}
	join := func(a, b int) {
		ra, rb := d.find(a), d.find(b)
		if ra == rb {
			return
		}
		r := d.union(ra, rb)
		o := ra + rb - r
		for m := range macs[o] {
			macs[r][m] = true
		}
	}

	// Stage 1: MAC. Observations sharing a MAC are the same host.
	byMAC := map[string]int{}
	for i, o := range all {
		for _, m := range o.macs {
			if j, ok := byMAC[m]; ok {
				join(i, j)
			} else {
				byMAC[m] = i
			}
		}
	}
	// Stage 2 and 3: name, then IP. Clusters with differing MACs never merge.
	stage := func(keys func(o obs) []string) {
		index := map[string][]int{}
		for i, o := range all {
			for _, k := range keys(o) {
				index[k] = append(index[k], i)
			}
		}
		names := make([]string, 0, len(index))
		for k := range index {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			var anchored, loose []int
			seen := map[int]bool{}
			for _, i := range index[k] {
				r := d.find(i)
				if seen[r] {
					continue
				}
				seen[r] = true
				if len(macs[r]) > 0 {
					anchored = append(anchored, r)
				} else {
					loose = append(loose, r)
				}
			}
			for _, r := range loose[min(1, len(loose)):] {
				join(loose[0], r)
			}
			// A MAC-less cluster joins a MAC-bearing one only when that is
			// the only candidate; with several it stays separate.
			if len(anchored) == 1 && len(loose) > 0 {
				join(anchored[0], loose[0])
			}
		}
	}
	stage(func(o obs) []string { return o.names })
	stage(func(o obs) []string { return o.ips })

	// Collect clusters in canonical order (smallest member index first).
	var order []int
	members := map[int][]int{}
	for i := range all {
		r := d.find(i)
		if _, ok := members[r]; !ok {
			order = append(order, r)
		}
		members[r] = append(members[r], i)
	}
	hosts := make([]Host, 0, len(order))
	for _, r := range order {
		hosts = append(hosts, buildHost(all, members[r]))
	}
	sort.SliceStable(hosts, func(i, j int) bool { return lessHost(hosts[i], hosts[j]) })
	return Result{Hosts: hosts, Conflicts: findConflicts(hosts, now)}
}

func lessHost(a, b Host) bool {
	ka, aok := a.Strongest()
	kb, bok := b.Strongest()
	switch {
	case aok != bok:
		return aok
	case !aok:
		return a.Name < b.Name
	case keyRank(ka.Kind) != keyRank(kb.Kind):
		return keyRank(ka.Kind) < keyRank(kb.Kind)
	}
	return ka.Value < kb.Value
}

func keyRank(kind string) int {
	switch kind {
	case KeyMAC:
		return 0
	case KeyName:
		return 1
	}
	return 2
}

// normalize fills the normalized MAC, name and IP lists. A short name (no
// dot) is resolved to the one FQDN with that first label when it is
// unambiguous across all observations; otherwise it only matches itself.
func normalize(all []obs) {
	fqdns := map[string]map[string]bool{} // first label -> distinct FQDNs
	note := func(n string) {
		if label, _, ok := strings.Cut(n, "."); ok && label != "" {
			if fqdns[label] == nil {
				fqdns[label] = map[string]bool{}
			}
			fqdns[label][n] = true
		}
	}
	for _, o := range all {
		note(normName(o.Fields.Hostname))
		note(normName(o.Fields.FQDN))
	}
	for i := range all {
		o := &all[i]
		for _, m := range o.Fields.MACs {
			if m = normMAC(m); m != "" && !slices.Contains(o.macs, m) {
				o.macs = append(o.macs, m)
			}
		}
		for _, ip := range o.Fields.IPs {
			if ip = normIP(ip); ip != "" && !slices.Contains(o.ips, ip) {
				o.ips = append(o.ips, ip)
			}
		}
		host, fqdn := normName(o.Fields.Hostname), normName(o.Fields.FQDN)
		if label, _, ok := strings.Cut(fqdn, "."); ok && host == label {
			host = "" // the FQDN already names this short name
		}
		for _, n := range []string{fqdn, host} {
			if n == "" {
				continue
			}
			if !strings.Contains(n, ".") && len(fqdns[n]) == 1 {
				for f := range fqdns[n] {
					n = f
				}
			}
			if !slices.Contains(o.names, n) {
				o.names = append(o.names, n)
			}
		}
	}
}

func sourceOf(o obs) Source {
	return Source{ConnectorID: o.ConnectorID, ConnectorName: o.ConnectorName, ConnectorType: o.ConnectorType}
}

// buildHost merges the members (already in priority order) of one cluster.
func buildHost(all []obs, idx []int) Host {
	h := Host{Observations: len(idx)}
	for _, i := range idx {
		h.Members = append(h.Members, MemberRef{ConnectorID: all[i].ConnectorID, ExternalKey: all[i].ExternalKey})
	}
	f := &h.Fields
	f.Sources = map[string][]Source{}
	addSource := func(field string, s Source) {
		if !slices.Contains(f.Sources[field], s) {
			f.Sources[field] = append(f.Sources[field], s)
		}
	}
	scalar := func(field string, dst *string, v string, o obs) {
		if *dst == "" && v != "" {
			*dst = v
			f.Sources[field] = []Source{sourceOf(o)}
		}
	}
	list := func(field string, dst *[]string, vals []string, o obs) {
		for _, v := range vals {
			if !slices.Contains(*dst, v) {
				*dst = append(*dst, v)
				addSource(field, sourceOf(o))
			}
		}
	}
	var names []string
	for n, i := range idx {
		o := all[i]
		scalar("kind", &f.Kind, o.Kind, o)
		scalar("hostname", &f.Hostname, strings.TrimSpace(o.Fields.Hostname), o)
		scalar("fqdn", &f.FQDN, strings.TrimSpace(o.Fields.FQDN), o)
		scalar("notes", &f.Notes, o.Fields.Notes, o)
		scalar("status", &f.Status, o.Fields.Status, o)
		scalar("interface", &f.Interface, o.Fields.Interface, o)
		switch o.Kind {
		case connectors.KindLease, connectors.KindARP, connectors.KindNDP:
			if f.NetSeen == nil || o.LastSeen.After(*f.NetSeen) {
				t := o.LastSeen.UTC()
				f.NetSeen = &t
			}
		case connectors.KindVM, connectors.KindLXC, connectors.KindDevice:
			if f.Power == "" {
				f.Power = strings.ToLower(strings.TrimSpace(o.Fields.Status))
			}
		}
		list("macs", &f.MACs, o.macs, o)
		list("ips", &f.IPs, o.ips, o)
		list("tags", &f.Tags, o.Fields.Tags, o)
		for k, v := range o.Fields.Attrs {
			if v == "" {
				continue
			}
			if f.Attrs == nil {
				f.Attrs = map[string]string{}
			}
			if _, ok := f.Attrs[k]; !ok {
				f.Attrs[k] = v
			}
		}
		list("", &names, o.names, o)
		if n == 0 || o.FirstSeen.Before(h.FirstSeen) {
			h.FirstSeen = o.FirstSeen
		}
		if o.LastSeen.After(h.LastSeen) {
			h.LastSeen = o.LastSeen
		}
	}
	delete(f.Sources, "")
	h.Name = f.Hostname
	if h.Name == "" {
		h.Name = f.FQDN
	}
	for _, m := range sortedCopy(f.MACs) {
		h.Keys = append(h.Keys, Key{KeyMAC, m})
	}
	for _, n := range sortedCopy(names) {
		h.Keys = append(h.Keys, Key{KeyName, n})
	}
	for _, ip := range sortedCopy(f.IPs) {
		h.Keys = append(h.Keys, Key{KeyIP, ip})
	}
	return h
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

// keyValues returns the sorted values of one key kind.
func (h Host) keyValues(kind string) []string {
	var out []string
	for _, k := range h.Keys {
		if k.Kind == kind {
			out = append(out, k.Value)
		}
	}
	return out
}

// anchor is the stable identifier of a host inside conflict fingerprints:
// its smallest MAC, else its strongest key.
func (h Host) anchor() string {
	k, ok := h.Strongest()
	if !ok {
		return "host:" + h.Name
	}
	return k.String()
}

func findConflicts(hosts []Host, now time.Time) []Conflict {
	var out []Conflict
	add := func(typ string, keys []string, idx []int, details map[string]any) {
		sort.Strings(keys)
		out = append(out, Conflict{Type: typ, Fingerprint: typ + "|" + strings.Join(keys, "|"), Hosts: idx, Details: details})
	}
	// Same IP / same name on several hosts. Merge only lets this happen for
	// hosts whose MACs differ.
	for _, kind := range []struct{ key, typ, field string }{
		{KeyIP, ConflictIPMAC, "ip"}, {KeyName, ConflictDuplicateHostname, "name"},
	} {
		holders := map[string][]int{}
		for i, h := range hosts {
			for _, v := range h.keyValues(kind.key) {
				holders[v] = append(holders[v], i)
			}
		}
		values := make([]string, 0, len(holders))
		for v, hs := range holders {
			if len(hs) > 1 {
				values = append(values, v)
			}
		}
		sort.Strings(values)
		for _, v := range values {
			keys := []string{kind.key + ":" + v}
			var macs []string
			for _, i := range holders[v] {
				keys = append(keys, hosts[i].anchor())
				macs = append(macs, hosts[i].keyValues(KeyMAC)...)
			}
			keys = dedupe(keys)
			add(kind.typ, keys, holders[v], map[string]any{kind.field: v, "macs": sortedCopy(macs)})
		}
	}
	for i, h := range hosts {
		if !h.LastSeen.IsZero() && now.Sub(h.LastSeen) > DisappearedAfter {
			add(ConflictDisappeared, []string{h.anchor()}, []int{i}, map[string]any{
				"name": h.Name, "last_seen": h.LastSeen.UTC().Format(time.RFC3339),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out
}

func dedupe(s []string) []string {
	s = sortedCopy(s)
	return slices.Compact(s)
}
