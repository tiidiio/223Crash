package security

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type fakeResolver struct {
	addresses []netip.Addr
	err       error
}

func (f fakeResolver) LookupNetIP(
	_ context.Context,
	_, _ string,
) ([]netip.Addr, error) {
	return f.addresses, f.err
}

func mustAddr(t *testing.T, value string) netip.Addr {
	t.Helper()

	addr, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", value, err)
	}

	return addr
}

func TestIsPublicIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"IPv4 publique", "1.1.1.1", true},
		{"IPv4 privée 10", "10.0.0.1", false},
		{"IPv4 privée 172", "172.16.0.1", false},
		{"IPv4 privée 192", "192.168.1.1", false},
		{"Loopback IPv4", "127.0.0.1", false},
		{"Link-local IPv4", "169.254.1.1", false},
		{"CGNAT", "100.64.0.1", false},
		{"Documentation IPv4", "192.0.2.1", false},
		{"Benchmark IPv4", "198.18.0.1", false},
		{"Multicast IPv4", "224.0.0.1", false},
		{"IPv6 publique", "2606:4700:4700::1111", true},
		{"IPv6 loopback", "::1", false},
		{"IPv6 privée", "fd00::1", false},
		{"IPv6 link-local", "fe80::1", false},
		{"IPv6 documentation", "2001:db8::1", false},
		{"IPv6 multicast", "ff02::1", false},
		{"IPv4 mappée privée", "::ffff:192.168.1.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPublicIP(mustAddr(t, tt.ip)); got != tt.want {
				t.Fatalf("IsPublicIP(%s) = %v, attendu %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestValidatePublicHTTPSURLWithResolver(t *testing.T) {
	publicIPv4 := mustAddr(t, "1.1.1.1")
	publicIPv6 := mustAddr(t, "2606:4700:4700::1111")
	privateIPv4 := mustAddr(t, "10.0.0.8")

	tests := []struct {
		name      string
		raw       string
		resolver  fakeResolver
		wantError bool
	}{
		{
			name:     "HTTPS public",
			raw:      "https://wallet.example.com/api",
			resolver: fakeResolver{addresses: []netip.Addr{publicIPv4}},
		},
		{
			name:     "HTTPS avec IPv6 publique",
			raw:      "https://wallet.example.com/api",
			resolver: fakeResolver{addresses: []netip.Addr{publicIPv6}},
		},
		{
			name:      "HTTP refusé",
			raw:       "http://wallet.example.com",
			wantError: true,
		},
		{
			name:      "URL vide",
			raw:       "",
			wantError: true,
		},
		{
			name:      "espaces périphériques",
			raw:       " https://wallet.example.com",
			wantError: true,
		},
		{
			name:      "identifiants interdits",
			raw:       "https://user:pass@wallet.example.com",
			wantError: true,
		},
		{
			name:      "fragment interdit",
			raw:       "https://wallet.example.com/path#fragment",
			wantError: true,
		},
		{
			name:      "hôte absent",
			raw:       "https://",
			wantError: true,
		},
		{
			name:      "nom local",
			raw:       "https://localhost",
			wantError: true,
		},
		{
			name:      "suffixe interne",
			raw:       "https://wallet.internal",
			wantError: true,
		},
		{
			name:      "nom DNS simple",
			raw:       "https://wallet",
			wantError: true,
		},
		{
			name:      "IP privée littérale",
			raw:       "https://10.0.0.8",
			wantError: true,
		},
		{
			name:      "IP loopback littérale",
			raw:       "https://127.0.0.1",
			wantError: true,
		},
		{
			name:      "DNS privé",
			raw:       "https://wallet.example.com",
			resolver:  fakeResolver{addresses: []netip.Addr{privateIPv4}},
			wantError: true,
		},
		{
			name:      "DNS mixte public et privé",
			raw:       "https://wallet.example.com",
			resolver:  fakeResolver{addresses: []netip.Addr{publicIPv4, privateIPv4}},
			wantError: true,
		},
		{
			name:      "DNS sans adresse",
			raw:       "https://wallet.example.com",
			resolver:  fakeResolver{},
			wantError: true,
		},
		{
			name:      "port invalide",
			raw:       "https://wallet.example.com:99999",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublicHTTPSURLWithResolver(
				context.Background(),
				tt.raw,
				tt.resolver,
			)

			if (err != nil) != tt.wantError {
				t.Fatalf(
					"ValidatePublicHTTPSURLWithResolver(%q) erreur = %v, wantError = %v",
					tt.raw,
					err,
					tt.wantError,
				)
			}
		})
	}
}

func TestValidatePublicHTTPSURLRejectsDNSFailure(t *testing.T) {
	resolver := fakeResolver{
		err: errors.New("DNS indisponible"),
	}

	err := ValidatePublicHTTPSURLWithResolver(
		context.Background(),
		"https://wallet.example.com",
		resolver,
	)

	if err == nil {
		t.Fatal("une erreur DNS doit entraîner un refus")
	}
}
