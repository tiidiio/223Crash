package security

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type IPResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

var blockedPrefixes = []netip.Prefix{
	// IPv4 : réseaux spéciaux, privés, de documentation et de test.
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),

	// IPv6 : plages spéciales, transition, documentation et non routables.
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

var blockedHostSuffixes = []string{
	".localhost",
	".local",
	".internal",
	".lan",
	".home",
	".test",
	".example",
	".invalid",
	".onion",
}

// IsPublicIP indique si une adresse est globalement routable selon la
// politique de sécurité de ce package.
// Une adresse privée, locale, spéciale ou réservée est toujours refusée.
func IsPublicIP(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}

	addr = addr.Unmap()

	if !addr.IsGlobalUnicast() ||
		addr.IsPrivate() ||
		addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() {
		return false
	}

	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}

	return true
}

// ValidatePublicHTTPSURL vérifie la syntaxe, la destination et les
// adresses DNS d'une URL HTTPS destinée à être appelée depuis le serveur.
func ValidatePublicHTTPSURL(ctx context.Context, raw string) error {
	return ValidatePublicHTTPSURLWithResolver(ctx, raw, net.DefaultResolver)
}

// ValidatePublicHTTPSURLWithResolver permet d'injecter un résolveur,
// notamment dans les tests, sans dépendre du DNS réel.
func ValidatePublicHTTPSURLWithResolver(
	ctx context.Context,
	raw string,
	resolver IPResolver,
) error {
	if ctx == nil {
		return fmt.Errorf("contexte absent")
	}

	if resolver == nil {
		return fmt.Errorf("résolveur DNS absent")
	}

	if raw == "" || strings.TrimSpace(raw) != raw {
		return fmt.Errorf("URL vide ou contenant des espaces périphériques")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL invalide: %w", err)
	}

	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("le schéma HTTPS est obligatoire")
	}

	if u.Opaque != "" || u.Host == "" || u.User != nil {
		return fmt.Errorf("URL absolue avec hôte obligatoire, sans identifiants")
	}

	if u.Fragment != "" || u.RawFragment != "" {
		return fmt.Errorf("les fragments URL sont interdits")
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("nom d'hôte absent")
	}

	if err := validatePort(u); err != nil {
		return err
	}

	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		if !IsPublicIP(addr) {
			return fmt.Errorf("adresse IP non publique interdite")
		}
		return nil
	}

	if strings.Contains(host, ":") ||
		strings.ContainsAny(host, " /\\%") ||
		!strings.Contains(host, ".") {
		return fmt.Errorf("nom d'hôte non public ou invalide")
	}

	for _, suffix := range blockedHostSuffixes {
		if host == strings.TrimPrefix(suffix, ".") ||
			strings.HasSuffix(host, suffix) {
			return fmt.Errorf("nom d'hôte local ou réservé interdit")
		}
	}

	if err := validateDNSName(host); err != nil {
		return err
	}

	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("résolution DNS impossible: %w", err)
	}

	if len(addresses) == 0 {
		return fmt.Errorf("aucune adresse DNS retournée")
	}

	for _, addr := range addresses {
		if !IsPublicIP(addr) {
			return fmt.Errorf(
				"la résolution DNS de %q contient une adresse non publique",
				host,
			)
		}
	}

	return nil
}

func validatePort(u *url.URL) error {
	port := u.Port()
	if port == "" {
		return nil
	}

	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("port URL invalide")
	}

	return nil
}

func validateDNSName(host string) error {
	if len(host) > 253 {
		return fmt.Errorf("nom DNS trop long")
	}

	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("nom DNS invalide")
		}

		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("label DNS invalide")
		}

		for _, r := range label {
			if !((r >= 'a' && r <= 'z') ||
				(r >= '0' && r <= '9') ||
				r == '-') {
				return fmt.Errorf("caractère interdit dans le nom DNS")
			}
		}
	}

	return nil
}

// ParseOrigin valide une origine HTTP (scheme://host[:port]) pour CORS.
func ParseOrigin(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("origine vide")
	}
	if raw == "" || strings.Contains(raw, "#") || strings.Contains(raw, "?") {
		// query/fragment interdits
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("origine invalide: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("origine sans scheme ou host")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme origine invalide: %s", u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("identifiants interdits dans origine")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("chemin interdit dans origine")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("query/fragment interdit")
	}
	if u.Opaque != "" {
		return nil, fmt.Errorf("origine opaque")
	}
	if strings.HasSuffix(raw, ":") {
		return nil, fmt.Errorf("port vide")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("port origine invalide: %s", port)
		}
	}
	return u, nil
}
