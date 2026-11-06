package opnsense

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

// rows decodes both {"rows":[...]} envelopes and bare arrays.
type rows []map[string]any

func (r *rows) UnmarshalJSON(b []byte) error {
	var env struct {
		Rows []map[string]any `json:"rows"`
	}
	if len(b) > 0 && b[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*r = arr
		return nil
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	*r = env.Rows
	return nil
}

// str returns the first non-empty string value of the given keys.
func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// macs returns the normalized MAC as a one-element list, or nil.
func macs(s string) []string {
	if m := normMAC(s); m != "" {
		return []string{m}
	}
	return nil
}

func normMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 17 || strings.Count(s, ":") != 5 {
		return ""
	}
	return s
}

// ips splits a comma/space separated address list and keeps valid addresses.
func ips(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if a, err := netip.ParseAddr(strings.TrimSpace(f)); err == nil {
			out = append(out, a.String())
		}
	}
	return out
}

// builder collects observations and drops duplicate keys.
type builder struct {
	res  *connectors.Result
	seen map[string]bool
}

func newBuilder(res *connectors.Result) *builder { return &builder{res: res, seen: map[string]bool{}} }

func (b *builder) add(kind, source, uniq string, f connectors.Fields, iface string) {
	f.Hostname = cleanHostname(f.Hostname)
	if len(f.MACs) == 0 && len(f.IPs) == 0 && f.Hostname == "" {
		return
	}
	if uniq == "" {
		uniq = strings.Join(f.MACs, ",") + "/" + strings.Join(f.IPs, ",")
	}
	key := kind + ":" + source + ":" + uniq
	if b.seen[key] {
		return
	}
	b.seen[key] = true
	if iface != "" {
		f.Interface = iface
	}
	b.res.Observations = append(b.res.Observations, connectors.HostObservation{ExternalKey: key, Kind: kind, Fields: f})
}

// cleanHostname drops placeholders that DHCP servers report for clients
// without a name (dnsmasq uses "*").
func cleanHostname(h string) string {
	h = strings.TrimSpace(h)
	switch h {
	case "*", "-", "?":
		return ""
	}
	return h
}

// networks reads interfaces and VLANs. Both are optional.
func (c *conn) networks(ctx context.Context, res *connectors.Result) error {
	var ifs struct {
		Rows rows `json:"rows"`
	}
	okIf, err := c.optional(ctx, res, "interfaces/overview/interfaces_info", &ifs)
	if err != nil {
		return err
	}
	var vl rows
	okVl, err := c.optional(ctx, res, "interfaces/vlan_settings/search_item", &vl)
	if err != nil {
		return err
	}
	vlanByDev := map[string]int{}
	if okVl {
		for _, v := range vl {
			if tag, err := strconv.Atoi(str(v, "tag")); err == nil {
				if dev := str(v, "vlanif"); dev != "" {
					vlanByDev[dev] = tag
				}
			}
		}
	}
	if !okIf {
		return nil
	}
	for _, r := range ifs.Rows {
		ident, dev := str(r, "identifier"), str(r, "device")
		name := ident
		if name == "" {
			name = dev
		}
		var vlan *int
		if tag, err := strconv.Atoi(str(r, "vlan_tag")); err == nil && tag > 0 {
			vlan = &tag
		} else if tag, ok := vlanByDev[dev]; ok {
			vlan = &tag
		}
		for _, field := range []string{"ipv4", "ipv6"} {
			for _, cidr := range addrList(r[field]) {
				p, err := netip.ParsePrefix(cidr)
				if err != nil || p.Addr().IsLoopback() || p.Addr().IsLinkLocalUnicast() {
					continue
				}
				res.Networks = append(res.Networks, connectors.Network{
					ExternalKey: "if:" + name + ":" + p.Masked().String(), CIDR: p.Masked(), VLAN: vlan,
					Interface: name, Description: str(r, "description"), Source: Type,
				})
			}
		}
	}
	return nil
}

// addrList extracts CIDR strings from [{"ipaddr":"a/b"}] or ["a/b"].
func addrList(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, e := range arr {
		switch x := e.(type) {
		case string:
			out = append(out, x)
		case map[string]any:
			if s := str(x, "ipaddr", "address"); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func (c *conn) isc(ctx context.Context, res *connectors.Result, b *builder) error {
	var r struct {
		Rows rows `json:"rows"`
	}
	ok, err := c.optional(ctx, res, "dhcpv4/leases/search_lease", &r)
	if err != nil || !ok {
		return err
	}
	for _, row := range r.Rows {
		kind := connectors.KindLease
		if strings.EqualFold(str(row, "type"), "static") {
			kind = connectors.KindStatic
		}
		f := connectors.Fields{MACs: macs(str(row, "mac")), Hostname: str(row, "hostname"), IPs: ips(str(row, "address")),
			Notes: str(row, "descr"), Status: str(row, "status")}
		b.add(kind, BackendISC, "", f, str(row, "if", "if_descr"))
	}
	return nil
}

func (c *conn) dnsmasq(ctx context.Context, res *connectors.Result, b *builder) error {
	var l rows
	if ok, err := c.optional(ctx, res, "dnsmasq/leases/search", &l); err != nil {
		return err
	} else if ok {
		for _, row := range l {
			f := connectors.Fields{MACs: macs(str(row, "hwaddr", "mac")), Hostname: str(row, "hostname"), IPs: ips(str(row, "address"))}
			b.add(connectors.KindLease, BackendDnsmasq, "", f, str(row, "if_name", "if"))
		}
	}
	var h rows
	if ok, err := c.optional(ctx, res, "dnsmasq/settings/search_host", &h); err != nil {
		return err
	} else if ok {
		for _, row := range h {
			name := str(row, "host")
			f := connectors.Fields{Hostname: name, IPs: ips(str(row, "ip")), Notes: str(row, "descr")}
			if d := str(row, "domain"); d != "" && name != "" {
				f.FQDN = name + "." + d
			}
			hw := strings.FieldsFunc(str(row, "hwaddr"), func(r rune) bool { return r == ',' || r == ' ' })
			if len(hw) == 0 {
				b.add(connectors.KindStatic, BackendDnsmasq, str(row, "uuid"), f, "")
				continue
			}
			for i, m := range hw {
				f.MACs = macs(m)
				b.add(connectors.KindStatic, BackendDnsmasq, fmt.Sprintf("%s#%d", str(row, "uuid"), i), f, "")
			}
		}
	}
	return nil
}

func (c *conn) kea(ctx context.Context, res *connectors.Result, b *builder) error {
	var l rows
	if ok, err := c.optional(ctx, res, "kea/leases4/search", &l); err != nil {
		return err
	} else if ok {
		for _, row := range l {
			f := connectors.Fields{MACs: macs(str(row, "hwaddr", "hw_address")), Hostname: str(row, "hostname"), IPs: ips(str(row, "address")), Status: str(row, "state")}
			b.add(connectors.KindLease, BackendKea, "", f, str(row, "if_name", "if_descr"))
		}
	}
	var rs rows
	if ok, err := c.optional(ctx, res, "kea/dhcpv4/search_reservation", &rs); err != nil {
		return err
	} else if ok {
		for _, row := range rs {
			f := connectors.Fields{MACs: macs(str(row, "hw_address")), Hostname: str(row, "hostname"), IPs: ips(str(row, "ip_address")), Notes: str(row, "description")}
			b.add(connectors.KindStatic, BackendKea, str(row, "uuid"), f, "")
		}
	}
	var sn rows
	if ok, err := c.optional(ctx, res, "kea/dhcpv4/search_subnet", &sn); err != nil {
		return err
	} else if ok {
		known := map[netip.Prefix]bool{}
		for _, n := range res.Networks {
			known[n.CIDR] = true
		}
		for _, row := range sn {
			p, err := netip.ParsePrefix(str(row, "subnet"))
			if err != nil || known[p.Masked()] {
				continue
			}
			res.Networks = append(res.Networks, connectors.Network{ExternalKey: "kea:" + p.Masked().String(), CIDR: p.Masked(),
				Description: str(row, "description"), Source: Type})
		}
	}
	return nil
}

// neighbours reads the ARP and NDP tables (both optional).
func (c *conn) neighbours(ctx context.Context, res *connectors.Result, b *builder) error {
	for _, e := range []struct{ endpoint, kind string }{
		{"diagnostics/interface/get_arp", connectors.KindARP}, {"diagnostics/interface/get_ndp", connectors.KindNDP},
	} {
		var r rows
		ok, err := c.optional(ctx, res, e.endpoint, &r)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		for _, row := range r {
			f := connectors.Fields{MACs: macs(str(row, "mac")), Hostname: str(row, "hostname"), IPs: ips(str(row, "ip"))}
			b.add(e.kind, "neigh", "", f, str(row, "intf_description", "intf"))
		}
	}
	return nil
}
