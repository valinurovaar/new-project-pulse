package urlcheck

import (
	"context"
	"errors"
	"net"
	"testing"
)

type resolver struct {
	ips []net.IP
	err error
}

func (r resolver) LookupIP(context.Context, string, string) ([]net.IP, error) { return r.ips, r.err }
func TestNormalize(t *testing.T) {
	got, e := Normalize(context.Background(), "HTTPS://Example.COM:443", resolver{ips: []net.IP{net.ParseIP("93.184.216.34")}})
	if e != nil || got != "https://example.com/" {
		t.Fatalf("got %q %v", got, e)
	}
}
func TestNormalizeTrailingDotAndPort(t *testing.T) {
	got, e := Normalize(context.Background(), "http://Example.COM.:8080", resolver{ips: []net.IP{net.ParseIP("93.184.216.34")}})
	if e != nil || got != "http://example.com:8080/" {
		t.Fatalf("got %q %v", got, e)
	}
}
func TestNormalizeIPv6AndPorts(t *testing.T) {
	r := resolver{ips: []net.IP{net.ParseIP("2001:4860:4860::8888")}}
	got, e := Normalize(context.Background(), "https://[2001:4860:4860::8888]:443", r)
	if e != nil || got != "https://[2001:4860:4860::8888]/" {
		t.Fatalf("IPv6 got %q %v", got, e)
	}
	got, e = Normalize(context.Background(), "http://example.com:080", resolver{ips: []net.IP{net.ParseIP("8.8.8.8")}})
	if e != nil || got != "http://example.com/" {
		t.Fatalf("port got %q %v", got, e)
	}
	if _, e = Normalize(context.Background(), "http://example.com:99999", resolver{ips: []net.IP{net.ParseIP("8.8.8.8")}}); e == nil {
		t.Fatal("out-of-range port accepted")
	}
}
func TestRejectUnsafeURLs(t *testing.T) {
	cases := []struct {
		url string
		r   resolver
	}{{"ftp://example.com", resolver{}}, {"http://user:pass@example.com", resolver{ips: []net.IP{net.ParseIP("93.184.216.34")}}}, {"http://localhost", resolver{ips: []net.IP{net.ParseIP("127.0.0.1")}}}, {"http://example.com", resolver{ips: []net.IP{net.ParseIP("10.0.0.1")}}}, {"http://example.com", resolver{ips: []net.IP{net.ParseIP("100.64.0.1")}}}, {"http://example.com", resolver{ips: []net.IP{net.ParseIP("0.0.0.1")}}}, {"http://example.com", resolver{err: errors.New("dns")}}}
	for _, c := range cases {
		if _, e := Normalize(context.Background(), c.url, c.r); e == nil {
			t.Fatalf("accepted %s", c.url)
		}
	}
}
