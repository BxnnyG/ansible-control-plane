// Package grouprules implements group rules (INV-5, ADR-0018): a rule maps the
// fleet hosts that match a criterion into one group of an inventory.
//
// Matching, naming and diffing are pure functions (matcher.go, naming.go,
// diff.go). The Service stores rules, previews the diff and applies it
// explicitly; it never applies anything on its own.
//
// Host naming (documented rule): a fleet host becomes the inventory host named
// by its FQDN (only a name containing a dot counts as an FQDN, and an FQDN is
// assumed to resolve, so no ansible_host is set). Otherwise the short hostname
// is used with ansible_host set to the primary IP, because a short name is not
// known to resolve. A host without any name is named by its primary IP and
// needs no ansible_host. The primary IP is the first IPv4 address, else the
// first address. Existing inventory hosts that already represent the fleet
// host (same name or FQDN/hostname, or ansible_host equal to one of its
// addresses) are reused and never modified.
//
// Modes: "add-only" only adds memberships. "sync" also removes the hosts the
// rule added earlier and that no longer match. The memberships a rule added are
// recorded (group_rule_members), so memberships that existed before or were
// created manually are never removed.
package grouprules
