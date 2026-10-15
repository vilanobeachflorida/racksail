// Package network manages the server's network: interfaces and IP addresses
// with their roles, NAT mapping, gateways and policy routing, IPv6, bonds,
// VLANs and bridges, the default-deny firewall with brute-force bans, the
// outbound IP of each account, and reverse DNS and blocklist checks. It is
// required and always on.
//
// Its screens live in ui/src/modules/network and its documentation in
// docs/site/src/content/docs/modules/network.md.
package network
