package config

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type Admin struct {
	AllowedNetworks []string `yaml:"allowed_networks"`
	AllowedHosts    []string `yaml:"allowed_hosts"`
}

// PrivateNetwork permits only loopback and RFC 1918 / IPv6 ULA networks.
// Forwarded client addresses must never be used to evaluate this policy.
func PrivateNetwork(prefix netip.Prefix) bool {
	for _, text := range []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		root := netip.MustParsePrefix(text)
		if prefix.Addr().BitLen() == root.Addr().BitLen() && prefix.Bits() >= root.Bits() && root.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}
func (c *Config) validateListeners() error {
	if c.Listen == "" {
		return fmt.Errorf("admin listen address must not be empty")
	}
	for _, address := range []string{c.Listen, c.PublicListen} {
		if address == "" {
			continue
		}
		_, port, err := net.SplitHostPort(address)
		number, parseErr := strconv.Atoi(port)
		if err != nil || parseErr != nil || number < 1 || number > 65535 {
			return fmt.Errorf("listener must be a host:port address with port 1–65535")
		}
	}
	if c.PublicListen != "" && c.PublicListen == c.Listen {
		return fmt.Errorf("admin and public listeners must use separate addresses")
	}
	if len(c.Admin.AllowedNetworks) == 0 {
		return fmt.Errorf("admin.allowed_networks must contain at least one private or loopback network")
	}
	for _, text := range c.Admin.AllowedNetworks {
		prefix, err := netip.ParsePrefix(text)
		if err != nil || !PrivateNetwork(prefix) {
			return fmt.Errorf("admin.allowed_networks accepts only private or loopback CIDRs: %q", text)
		}
	}
	for _, host := range c.Admin.AllowedHosts {
		if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && (addr.IsPrivate() || addr.IsLoopback()) {
			continue
		}
		if host == "" || strings.ContainsAny(host, "/:*?@#\\ \t\r\n") {
			return fmt.Errorf("admin.allowed_hosts requires exact LAN hostnames or IPv4 addresses without ports")
		}
	}
	return nil
}

// ValidateServing fails closed before any HTTP listener or background worker starts.
// Load can still read incomplete configuration for setup and inspection.
func (c *Config) ValidateServing() error {
	if err := c.validateUsers(); err != nil {
		return err
	}
	if err := c.validateListeners(); err != nil {
		return err
	}
	if len(c.Users) == 0 && c.AuthPassword == "" {
		return fmt.Errorf("configure an admin account or auth_password; anonymous administration is disabled")
	}
	if c.PublicListen != "" {
		enabled := false
		for _, user := range c.Users {
			if !user.Disabled && user.Role == "user" {
				enabled = true
			}
		}
		if !enabled {
			return fmt.Errorf("public_listen requires at least one enabled users account with role user")
		}
	}
	return nil
}
