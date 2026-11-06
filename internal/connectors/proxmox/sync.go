package proxmox

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

// Tag forms: Proxmox tags become "proxmox:<tag>" and every guest/node also
// carries "node:<pve node>". Additional MACs (beyond Fields.MAC) become
// "mac:<mac>" tags.
const (
	tagPrefix  = "proxmox:"
	nodePrefix = "node:"
	macPrefix  = "mac:"
)

type node struct {
	Node   string `json:"node"`
	Status string `json:"status"`
}

type resource struct {
	Type     string `json:"type"` // qemu or lxc
	VMID     int    `json:"vmid"`
	Name     string `json:"name"`
	Node     string `json:"node"`
	Status   string `json:"status"`
	Tags     string `json:"tags"`
	Template int    `json:"template"`
}

type guestConfig struct {
	Description string `json:"description"`
	Tags        string `json:"tags"`
	Agent       string `json:"agent"`
}

// Sync reads the current state.
func (c *conn) Sync(ctx context.Context) (connectors.Result, error) {
	var res connectors.Result
	if _, err := c.version(ctx); err != nil {
		return res, err
	}
	var nodes []node
	if err := c.get(ctx, "nodes", nil, &nodes, true); err != nil {
		return res, fmt.Errorf("nodes: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })
	nodeIPs := map[string][]string{}
	for _, n := range nodes {
		ips, err := c.networks(ctx, &res, n)
		if err != nil {
			return res, err
		}
		nodeIPs[n.Node] = ips
	}
	for _, n := range nodes {
		res.Observations = append(res.Observations, connectors.HostObservation{
			ExternalKey: "node:" + Type + ":" + n.Node, Kind: connectors.KindNode,
			Fields: connectors.Fields{Hostname: n.Node, IPs: nodeIPs[n.Node], Status: n.Status},
		})
	}
	var guests []resource
	if _, err := c.optional(ctx, &res, "cluster/resources", url.Values{"type": {"vm"}}, &guests); err != nil {
		return res, err
	}
	sort.Slice(guests, func(i, j int) bool { return guests[i].VMID < guests[j].VMID })
	for _, g := range guests {
		if g.Type != "qemu" && g.Type != "lxc" {
			continue
		}
		if err := c.guest(ctx, &res, g); err != nil {
			return res, err
		}
	}
	return res, nil
}

// networks reads the node's bridges/VLANs (optional) and returns the node's
// own addresses. Offline nodes are skipped with a warning.
func (c *conn) networks(ctx context.Context, res *connectors.Result, n node) ([]string, error) {
	if n.Status != "" && n.Status != "online" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("node %s is %s (network skipped)", n.Node, n.Status))
		return nil, nil
	}
	var ifs []struct {
		Iface   string `json:"iface"`
		Type    string `json:"type"`
		CIDR    string `json:"cidr"`
		CIDR6   string `json:"cidr6"`
		VlanID  any    `json:"vlan-id"`
		Comment string `json:"comments"`
	}
	ok, err := c.optional(ctx, res, "nodes/"+n.Node+"/network", nil, &ifs)
	if err != nil || !ok {
		return nil, err
	}
	var ips []string
	seen := map[string]bool{}
	for _, i := range ifs {
		var vlan *int
		if v, err := strconv.Atoi(fmt.Sprint(i.VlanID)); err == nil && i.VlanID != nil {
			vlan = &v
		}
		for _, cidr := range []string{i.CIDR, i.CIDR6} {
			p, err := netip.ParsePrefix(cidr)
			if err != nil {
				continue
			}
			if a := p.Addr().String(); !seen[a] {
				seen[a] = true
				ips = append(ips, a)
			}
			key := "net:" + n.Node + ":" + i.Iface + ":" + p.Masked().String()
			res.Networks = append(res.Networks, connectors.Network{
				ExternalKey: key, CIDR: p.Masked(), VLAN: vlan, Interface: i.Iface,
				Description: strings.TrimSpace(i.Comment), Source: Type,
			})
		}
	}
	return ips, nil
}

// splitTags splits a Proxmox tag list (semicolon or comma separated).
func splitTags(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
		out = append(out, tagPrefix+t)
	}
	return out
}

func normMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 17 || strings.Count(s, ":") != 5 {
		return ""
	}
	return s
}

// configMACs extracts MACs from netN entries: QEMU "virtio=MAC,bridge=..." (any
// model name) and LXC "name=eth0,hwaddr=MAC,...".
func configMACs(cfg map[string]any) []string {
	var keys []string
	for k := range cfg {
		if strings.HasPrefix(k, "net") {
			if _, err := strconv.Atoi(k[3:]); err == nil {
				keys = append(keys, k)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, _ := strconv.Atoi(keys[i][3:])
		b, _ := strconv.Atoi(keys[j][3:])
		return a < b
	})
	var out []string
	for _, k := range keys {
		s, _ := cfg[k].(string)
		for _, kv := range strings.Split(s, ",") {
			_, val, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if m := normMAC(val); m != "" {
				out = append(out, m)
			}
		}
	}
	return out
}

func usableIP(s string) (string, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return "", false
	}
	return a.String(), true
}

func appendUnique(dst []string, v string) []string {
	for _, d := range dst {
		if d == v {
			return dst
		}
	}
	return append(dst, v)
}

func (c *conn) guest(ctx context.Context, res *connectors.Result, g resource) error {
	kind := connectors.KindVM
	if g.Type == "lxc" {
		kind = connectors.KindLXC
	}
	base := fmt.Sprintf("nodes/%s/%s/%d", url.PathEscape(g.Node), g.Type, g.VMID)
	status := g.Status
	if g.Template == 1 {
		status = "template"
	}
	f := connectors.Fields{Hostname: g.Name, Status: status}
	tags := splitTags(g.Tags)

	var raw map[string]any
	cfgOK, err := c.optional(ctx, res, base+"/config", nil, &raw)
	if err != nil {
		return err
	}
	var macs []string
	agentOn := true // unknown config: still try the agent once
	if cfgOK {
		var cfg guestConfig
		cfg.Description, _ = raw["description"].(string)
		cfg.Tags, _ = raw["tags"].(string)
		cfg.Agent = fmt.Sprint(raw["agent"])
		f.Notes = strings.TrimSpace(cfg.Description)
		if len(tags) == 0 { // resources list carried no tags
			tags = splitTags(cfg.Tags)
		}
		macs = configMACs(raw)
		agentOn = agentEnabled(cfg.Agent)
	}

	if g.Status == "running" && g.Template != 1 {
		var ips, aMACs []string
		switch g.Type {
		case "qemu":
			if agentOn {
				ips, aMACs, err = c.agentIPs(ctx, res, base)
			}
		default:
			ips, aMACs, err = c.lxcIPs(ctx, res, base)
		}
		if err != nil {
			return err
		}
		f.IPs = ips
		for _, m := range aMACs {
			if !cfgOK {
				macs = appendUnique(macs, m)
			}
		}
	}
	f.Tags = append(tags, nodePrefix+g.Node)
	for i, m := range macs {
		if i == 0 {
			f.MAC = m
		} else {
			f.Tags = append(f.Tags, macPrefix+m)
		}
	}
	res.Observations = append(res.Observations, connectors.HostObservation{
		ExternalKey: kind + ":" + Type + ":" + strconv.Itoa(g.VMID), Kind: kind, Fields: f,
	})
	return nil
}

// agentEnabled reports whether the config "agent" option enables the guest
// agent ("1", "enabled=1,fstrim_cloned_disks=1"). Missing means disabled.
func agentEnabled(v string) bool {
	if v == "" || v == "<nil>" {
		return false
	}
	for _, p := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(p, "=")
		if !ok {
			if k == "1" {
				return true
			}
			continue
		}
		if (k == "enabled" || k == "1") && val == "1" {
			return true
		}
	}
	return false
}

func (c *conn) agentIPs(ctx context.Context, res *connectors.Result, base string) (ips, macs []string, err error) {
	var out struct {
		Result []struct {
			Name string `json:"name"`
			HW   string `json:"hardware-address"`
			IPs  []struct {
				IP string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	ok, err := c.bestEffort(ctx, res, base+"/agent/network-get-interfaces", "guest agent not available", &out)
	if err != nil || !ok {
		return nil, nil, err
	}
	for _, i := range out.Result {
		if m := normMAC(i.HW); m != "" && i.Name != "lo" {
			macs = appendUnique(macs, m)
		}
		for _, a := range i.IPs {
			if ip, ok := usableIP(a.IP); ok {
				ips = appendUnique(ips, ip)
			}
		}
	}
	return ips, macs, nil
}

func (c *conn) lxcIPs(ctx context.Context, res *connectors.Result, base string) (ips, macs []string, err error) {
	var out []struct {
		Name   string `json:"name"`
		HWAddr string `json:"hwaddr"`
		Inet   string `json:"inet"`
		Inet6  string `json:"inet6"`
	}
	ok, err := c.bestEffort(ctx, res, base+"/interfaces", "interfaces unavailable", &out)
	if err != nil || !ok {
		return nil, nil, err
	}
	for _, i := range out {
		if i.Name == "lo" {
			continue
		}
		if m := normMAC(i.HWAddr); m != "" {
			macs = appendUnique(macs, m)
		}
		for _, s := range []string{i.Inet, i.Inet6} {
			a, _, _ := strings.Cut(s, "/")
			if ip, ok := usableIP(a); ok {
				ips = appendUnique(ips, ip)
			}
		}
	}
	return ips, macs, nil
}
