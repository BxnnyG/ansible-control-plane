package grouprules

import (
	"net/netip"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
)

// PrimaryIP returns the first IPv4 address of the host, else its first valid
// address, in canonical form; "" if there is none.
func PrimaryIP(ips []string) string {
	first := ""
	for _, s := range ips {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		a = a.Unmap()
		if a.Is4() {
			return a.String()
		}
		if first == "" {
			first = a.String()
		}
	}
	return first
}

// InventoryName derives the inventory host name and the ansible_host value for
// a fleet host (see the package documentation). ok is false when the host has
// neither a usable name nor an address.
func InventoryName(h Host) (name, ansibleHost string, ok bool) {
	ip := PrimaryIP(h.IPs)
	fqdn := strings.ToLower(strings.TrimSpace(h.FQDN))
	short := strings.ToLower(strings.TrimSpace(h.Hostname))
	switch {
	case strings.Contains(fqdn, ".") && inventory.ValidHostName(fqdn):
		return fqdn, "", true
	case fqdn != "" && !strings.Contains(fqdn, ".") && short == "":
		short = fqdn
	}
	if short != "" && inventory.ValidHostName(short) {
		return short, ip, true
	}
	if ip != "" {
		return ip, "", true
	}
	return "", "", false
}
