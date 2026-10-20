// Package urlcheck validates and normalizes public HTTP targets.
package urlcheck

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Resolver is the DNS capability required for public-address validation.
type Resolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}

// Normalize validates a public HTTP(S) URL and produces its canonical form.
func Normalize(ctx context.Context, raw string, resolver Resolver) (string, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return "", errors.New("URL length is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("only absolute HTTP(S) URLs are allowed")
	}
	if u.User != nil {
		return "", errors.New("credentials in URL are forbidden")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", errors.New("local addresses are forbidden")
	}
	ips, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return "", errors.New("host cannot be resolved")
	}
	for _, ip := range ips {
		if forbidden(ip) {
			return "", errors.New("private or service addresses are forbidden")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", errors.New("URL port is invalid")
		}
		port = strconv.Itoa(n)
	}
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
func forbidden(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, network := range reservedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

var reservedNetworks = func() []*net.IPNet {
	cidrs := []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "fec0::/10"}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, _ := net.ParseCIDR(cidr)
		out = append(out, network)
	}
	return out
}()
