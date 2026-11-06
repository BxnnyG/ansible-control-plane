package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

// tagPrefix marks NetBox tags in the merged tag list (like "proxmox:").
const tagPrefix = "netbox:"

type ref struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type valueRef struct {
	Value string `json:"value"`
}

type ipRef struct {
	Address string `json:"address"`
}

type macRef struct {
	MAC string `json:"mac_address"`
}

type object struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Status     valueRef `json:"status"`
	Tags       []ref    `json:"tags"`
	Site       *ref     `json:"site"`
	Role       *ref     `json:"role"`
	DeviceRole *ref     `json:"device_role"` // NetBox < 4.0
	Tenant     *ref     `json:"tenant"`
	Platform   *ref     `json:"platform"`
	Cluster    *ref     `json:"cluster"`
	PrimaryIP4 *ipRef   `json:"primary_ip4"`
	PrimaryIP6 *ipRef   `json:"primary_ip6"`
}

type iface struct {
	ID         int      `json:"id"`
	Device     *ref     `json:"device"`
	VM         *ref     `json:"virtual_machine"`
	MAC        string   `json:"mac_address"`
	PrimaryMAC *macRef  `json:"primary_mac_address"`
	MACs       []macRef `json:"mac_addresses"`
}

type ipAddress struct {
	Address string `json:"address"`
	Type    string `json:"assigned_object_type"`
	ObjID   *int   `json:"assigned_object_id"`
}

type prefix struct {
	ID          int    `json:"id"`
	Prefix      string `json:"prefix"`
	Description string `json:"description"`
	VLAN        *struct {
		VID int `json:"vid"`
	} `json:"vlan"`
}

// owner identifies a device or VM by observation kind and NetBox id.
type owner struct {
	kind string
	id   int
}

// Sync reads the current state.
func (c *conn) Sync(ctx context.Context) (connectors.Result, error) {
	var res connectors.Result
	if err := c.Test(ctx); err != nil {
		return res, err
	}
	err := c.optional(ctx, &res, "api/ipam/prefixes/", func(r json.RawMessage) error {
		var p prefix
		if err := json.Unmarshal(r, &p); err != nil {
			return fmt.Errorf("decode prefix: %w", err)
		}
		pfx, err := netip.ParsePrefix(strings.TrimSpace(p.Prefix))
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("prefix %d: invalid prefix skipped", p.ID))
			return nil
		}
		n := connectors.Network{
			ExternalKey: "prefix:" + strconv.Itoa(p.ID), CIDR: pfx.Masked(),
			Description: strings.TrimSpace(p.Description), Source: Type,
		}
		if p.VLAN != nil {
			v := p.VLAN.VID
			n.VLAN = &v
		}
		res.Networks = append(res.Networks, n)
		return nil
	})
	if err != nil {
		return res, err
	}

	// Interfaces: owner lookup per interface and MACs per owner.
	ifOwner := map[string]owner{} // "<object type>/<id>"
	macs := map[owner][]string{}
	addIfaces := func(endpoint, objType, kind string) error {
		return c.optional(ctx, &res, endpoint, func(r json.RawMessage) error {
			var i iface
			if err := json.Unmarshal(r, &i); err != nil {
				return fmt.Errorf("decode interface: %w", err)
			}
			var o owner
			switch {
			case i.Device != nil:
				o = owner{kind, i.Device.ID}
			case i.VM != nil:
				o = owner{kind, i.VM.ID}
			default:
				return nil
			}
			ifOwner[objType+"/"+strconv.Itoa(i.ID)] = o
			var list []string
			if i.PrimaryMAC != nil {
				list = append(list, i.PrimaryMAC.MAC)
			}
			list = append(list, i.MAC)
			for _, m := range i.MACs {
				list = append(list, m.MAC)
			}
			for _, m := range list {
				if m = normMAC(m); m != "" && !slices.Contains(macs[o], m) {
					macs[o] = append(macs[o], m)
				}
			}
			return nil
		})
	}
	if err := addIfaces("api/dcim/interfaces/", "dcim.interface", connectors.KindDevice); err != nil {
		return res, err
	}
	if err := addIfaces("api/virtualization/interfaces/", "virtualization.vminterface", connectors.KindVM); err != nil {
		return res, err
	}

	ips := map[owner][]string{}
	err = c.optional(ctx, &res, "api/ipam/ip-addresses/", func(r json.RawMessage) error {
		var a ipAddress
		if err := json.Unmarshal(r, &a); err != nil {
			return fmt.Errorf("decode ip address: %w", err)
		}
		if a.ObjID == nil {
			return nil
		}
		o, ok := ifOwner[a.Type+"/"+strconv.Itoa(*a.ObjID)]
		if !ok {
			return nil
		}
		if ip := stripMask(a.Address); ip != "" && !slices.Contains(ips[o], ip) {
			ips[o] = append(ips[o], ip)
		}
		return nil
	})
	if err != nil {
		return res, err
	}

	for _, e := range []struct{ endpoint, kind string }{
		{"api/dcim/devices/", connectors.KindDevice},
		{"api/virtualization/virtual-machines/", connectors.KindVM},
	} {
		err := c.optional(ctx, &res, e.endpoint, func(r json.RawMessage) error {
			var o object
			if err := json.Unmarshal(r, &o); err != nil {
				return fmt.Errorf("decode %s: %w", e.kind, err)
			}
			if obs, ok := observation(e.kind, o, ips[owner{e.kind, o.ID}], macs[owner{e.kind, o.ID}]); ok {
				res.Observations = append(res.Observations, obs)
			}
			return nil
		})
		if err != nil {
			return res, err
		}
	}
	sort.Slice(res.Observations, func(i, j int) bool { return res.Observations[i].ExternalKey < res.Observations[j].ExternalKey })
	sort.Slice(res.Networks, func(i, j int) bool { return res.Networks[i].ExternalKey < res.Networks[j].ExternalKey })
	return res, nil
}

func observation(kind string, o object, ifIPs, ifMACs []string) (connectors.HostObservation, bool) {
	name := strings.TrimSpace(o.Name)
	if name == "" {
		return connectors.HostObservation{}, false
	}
	f := connectors.Fields{Hostname: name, Status: o.Status.Value}
	if strings.Contains(name, ".") {
		f.FQDN = name
	}
	for _, p := range []*ipRef{o.PrimaryIP4, o.PrimaryIP6} {
		if p != nil {
			if ip := stripMask(p.Address); ip != "" && !slices.Contains(f.IPs, ip) {
				f.IPs = append(f.IPs, ip)
			}
		}
	}
	for _, ip := range ifIPs {
		if !slices.Contains(f.IPs, ip) {
			f.IPs = append(f.IPs, ip)
		}
	}
	f.MACs = ifMACs
	for _, t := range o.Tags {
		if s := strings.ToLower(strings.TrimSpace(t.Slug)); s != "" {
			f.Tags = append(f.Tags, tagPrefix+s)
		}
	}
	role := o.Role
	if role == nil {
		role = o.DeviceRole
	}
	for k, r := range map[string]*ref{"site": o.Site, "role": role, "tenant": o.Tenant, "platform": o.Platform, "cluster": o.Cluster} {
		if r != nil && strings.TrimSpace(r.Slug) != "" {
			if f.Attrs == nil {
				f.Attrs = map[string]string{}
			}
			f.Attrs[k] = strings.ToLower(strings.TrimSpace(r.Slug))
		}
	}
	return connectors.HostObservation{ExternalKey: kind + ":" + Type + ":" + strconv.Itoa(o.ID), Kind: kind, Fields: f}, true
}

// stripMask turns "192.0.2.10/24" into "192.0.2.10"; invalid input yields "".
func stripMask(s string) string {
	s = strings.TrimSpace(s)
	if a, _, ok := strings.Cut(s, "/"); ok {
		s = a
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	return ip.Unmap().String()
}

// normMAC returns the lower-case colon form of a MAC or "".
func normMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", ":", ".", "").Replace(s)
	if !strings.Contains(s, ":") && len(s) == 12 {
		var parts []string
		for i := 0; i < 12; i += 2 {
			parts = append(parts, s[i:i+2])
		}
		s = strings.Join(parts, ":")
	}
	if len(s) != 17 {
		return ""
	}
	for i, r := range s {
		if i%3 == 2 {
			if r != ':' {
				return ""
			}
		} else if !strings.ContainsRune("0123456789abcdef", r) {
			return ""
		}
	}
	return s
}
