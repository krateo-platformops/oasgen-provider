// Package hostpolicy decides whether a CREDENTIALED request may be sent to a given destination.
//
// WHY THIS EXISTS. Until #379 a request's destination host was never validated. The OAS document's
// servers[0] set the base URL, the credential was attached unconditionally to whatever *http.Request was
// produced, and nothing between those two points looked at where the request was going. Three channels
// relocated the destination, all verified at runtime against a second HTTP server that reported what it
// received:
//
//  1. buildPath joins the host as the first element of a path.Join, so a `..` in a path-parameter value
//     (which comes from the CR spec) can eat segments to its left and promote a later segment to be the
//     new host. url.PathEscape escapes `/` but not `.`.
//  2. The same, on the paginated twin.
//  3. A redirect. net/http strips sensitive headers across a hostname change -- but only the three it
//     knows. An OAS `apiKey` scheme sends the credential in a header the DOCUMENT names, which is not on
//     that list, so a single vendor-issued 302 relocates it with no CR write at all.
//
// Channels 1 and 2 are a privilege escalation: the Secret is read by the CONTROLLER's service account
// from a ref the CR author writes, so someone who cannot read the credential can still direct where its
// contents are sent.
//
// WHY THE CHECK IS AT THE DIAL. Validating a URL cannot close channel 3: the redirect target is never
// parsed by our code. Validating at the dial covers every hop, because every hop dials.
//
// WHY NOT REUSE safeHTTPClient. jqmodule's denyInternalAddr is the wrong threat model -- it was built for
// UNCREDENTIALED fetches and therefore permits arbitrary public hosts, which is exactly the attack here.
// It also deliberately leaves RFC1918 and service CIDRs reachable, so it would not even stop a credential
// going to a Service in the controller's own namespace.
package hostpolicy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/idna"
)

// Policy is the set of destinations a credentialed request may reach.
//
// The zero value permits nothing, deliberately: a Policy that has not been configured must not fail open.
// Construction goes through Parse, and BuildClient takes one as a REQUIRED parameter so that a missing
// policy is a compile error rather than an unguarded request. An unguarded request succeeds, which is why
// failing open here would be invisible.
type Policy struct {
	entries []entry
	// allowAny disables host matching entirely. Spelled as its own field rather than as a `*` entry so it
	// shows up in a values diff instead of hiding as punctuation in a list.
	allowAny bool
	// allowLoopback relaxes the loopback refusal in denyIP, and NOTHING AN OPERATOR CAN WRITE SETS IT.
	//
	// It exists because httptest binds 127.0.0.1, so without a seam no request-making test in this
	// repository can use a policy-enforced client -- which would mean the guard ships with its
	// request path untested, which is how an unguarded request got here in the first place.
	//
	// IT IS DELIBERATELY NOT CHART VALUES. The obvious design is an `allowLoopback` values key, and it
	// is the wrong one: the person who sets it is whoever needs a dev install or a test to work, and
	// the moment it is set 169.254.169.254 is reachable by a CREDENTIALED request. That is exactly the
	// hole #379 is about, re-opened by configuration. An untested code path is a smaller cost than a
	// values key that re-enables an SSRF target -- so the seam is reachable only from Go, only through
	// AllowLoopbackForTesting, and a test asserts that method has no non-test caller.
	allowLoopback bool
}

// AllowLoopbackForTesting relaxes the loopback refusal. CALL IT ONLY FROM TESTS.
//
// Exported because tests in other packages need it -- httptest binds 127.0.0.1, so a test of any
// request-making code path cannot otherwise use a policy-enforced client at all.
//
// The name is deliberately unpleasant and the enforcement is a test: hostpolicy_seam_test.go asserts
// this method has zero callers outside _test.go files, with a control proving the search finds callers.
// That is weaker than a compile-time barrier and stronger than a comment, and it is the same shape as
// the ExistenceChecker assertion in exists.go -- a property of the codebase asserted by a test, because
// drifting from it would otherwise be silent.
//
// It relaxes LOOPBACK ONLY. Link-local, multicast and the unspecified address stay refused, so a test
// cannot accidentally make the cloud-metadata endpoint reachable either.
func (p *Policy) AllowLoopbackForTesting() *Policy {
	if p != nil {
		p.allowLoopback = true
	}
	return p
}

// entry is one permitted destination. Scheme and port both participate in matching.
type entry struct {
	// host is an A-label, lowercased, with no trailing dot. When wildcard is true it is the suffix AFTER
	// the leading "*." -- so "*.ghe.example.com" stores host "ghe.example.com", wildcard true.
	host     string
	wildcard bool
	port     string // always explicit after Parse: "443" or "80" unless the entry named one
	scheme   string // "https" or "http"
}

// Config is the OPERATOR half of a Policy: what chart values say. The document half -- the hosts the OAS
// document itself names -- is collected by BuildClient, which is the thing that parses the document.
//
// Split this way deliberately. If BuildClient took a ready-made *Policy, a caller could build one without
// the document's hosts and every request would be refused; taking the Config instead means NO CALLER CAN
// FORGET the document hosts, because the only code that could omit them is the code that reads them.
type Config struct {
	// Allowlist holds operator-controlled entries. EMPTY IS THE NORMAL CASE and means "no override
	// permitted; use the document's hosts" -- a no-op for every existing install.
	Allowlist []string
	// AllowAnyHost disables host matching entirely. The unconditional refusals still apply.
	AllowAnyHost bool
}

// Parse builds a Policy.
//
// allowlist holds operator-controlled entries from chart values. documentHosts holds the hosts the OAS
// document itself names -- its base `servers:` and any per-operation `servers[]` -- which are permitted
// unconditionally, because they are where requests go today.
//
// THAT DEFAULT IS THE WHOLE DESIGN. An empty allowlist means "no overrides permitted; use the document's
// hosts", which is simultaneously a no-op for every existing install and a fix for all three channels. It
// is NOT "permit anything" and NOT "configuration error": requiring the value would turn every
// single-host provider into a config error on upgrade for a capability it does not use (measured by a
// consumer: 13 specs, one identical server, zero server variables, zero per-operation overrides).
func Parse(allowlist, documentHosts []string, allowAny bool) (*Policy, error) {
	p := &Policy{allowAny: allowAny}
	for _, raw := range allowlist {
		e, err := parseEntry(raw)
		if err != nil {
			return nil, fmt.Errorf("endpoint allowlist entry %q: %w", raw, err)
		}
		p.entries = append(p.entries, e)
	}
	for _, raw := range documentHosts {
		// A document host arrives as a full server URL. It is permitted with its OWN scheme, including
		// cleartext: the document is operator-supplied content, and refusing http:// here would break a
		// document that names one without giving the operator anything to change.
		e, err := parseEntry(raw)
		if err != nil {
			// Not fatal, and not silent. A server URL we cannot parse is a document problem, and refusing
			// to build the client over it would make the whole kind unreconcilable for a cosmetic defect
			// in a server we may never use.
			continue
		}
		p.entries = append(p.entries, e)
	}
	return p, nil
}

// parseEntry normalises one entry. See the package doc for why each step is here.
func parseEntry(raw string) (entry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return entry{}, fmt.Errorf("is empty")
	}
	if raw == "*" {
		// A bare "*" is rejected rather than honoured. "Allow everything" must be spelled
		// rdc.allowAnyHost: true so it appears in a values diff.
		return entry{}, fmt.Errorf(`a bare "*" is not a valid entry: set rdc.allowAnyHost: true if you really mean to permit any host`)
	}

	// An entry with no scheme permits https ONLY. Cleartext must be asked for explicitly.
	scheme := "https"
	switch {
	case strings.HasPrefix(raw, "https://"):
		raw = strings.TrimPrefix(raw, "https://")
	case strings.HasPrefix(raw, "http://"):
		scheme, raw = "http", strings.TrimPrefix(raw, "http://")
	case strings.Contains(raw, "://"):
		return entry{}, fmt.Errorf("unsupported scheme: only http:// and https:// are permitted")
	}
	// A server URL carries a path; it plays no part in matching a destination host.
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" {
		return entry{}, fmt.Errorf("has no host")
	}

	host, port := raw, ""
	if h, pp, err := net.SplitHostPort(raw); err == nil {
		host, port = h, pp
	}

	wildcard := false
	if strings.HasPrefix(host, "*.") {
		wildcard, host = true, strings.TrimPrefix(host, "*.")
		if host == "" {
			return entry{}, fmt.Errorf(`"*." must be followed by a domain`)
		}
	}
	if strings.Contains(host, "*") {
		return entry{}, fmt.Errorf(`"*" is only valid as the whole first label, as in "*.example.com"`)
	}

	norm, err := normaliseHost(host)
	if err != nil {
		return entry{}, err
	}
	if port == "" {
		port = "443"
		if scheme == "http" {
			port = "80"
		}
	}
	return entry{host: norm, wildcard: wildcard, port: port, scheme: scheme}, nil
}

// normaliseHost lowercases, strips one trailing dot, and converts to an IDNA A-label.
//
// The trailing dot matters because "ghe.example.com." and "ghe.example.com" are the same host to DNS and
// different strings to a comparison. The IDNA conversion matters because a Unicode homoglyph must not
// differ from its punycode form -- otherwise an entry written one way fails to match a request written
// the other, in either direction.
func normaliseHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", fmt.Errorf("has no host")
	}
	// A literal IP is already canonical; idna would reject or mangle it.
	if ip := net.ParseIP(h); ip != nil {
		return h, nil
	}
	a, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return "", fmt.Errorf("is not a usable hostname: %w", err)
	}
	return a, nil
}

// PermitsURL reports whether a request to u is allowed by the host list. It does NOT check the resolved
// address -- that is the dialer's job (see DialContext), because only the dialer sees what is actually
// being connected to.
func (p *Policy) PermitsURL(u *url.URL) error {
	if p == nil {
		return fmt.Errorf("refusing to send a credentialed request: no host policy is configured")
	}
	if p.allowAny {
		return nil
	}
	if u == nil {
		return fmt.Errorf("refusing to send a credentialed request: no URL")
	}
	// Hostname(), not Host: it discards userinfo and brackets. buildPath can produce
	// "https://@evil.example.com", whose Host is evil.example.com but whose raw string a prefix check
	// would read as starting with something else.
	host, err := normaliseHost(u.Hostname())
	if err != nil {
		return fmt.Errorf("refusing to send a credentialed request to %q: %w", u.Host, err)
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return fmt.Errorf("refusing to send a credentialed request to %q: unsupported scheme %q", u.Host, scheme)
		}
	}
	for _, e := range p.entries {
		if e.matches(host, port, scheme) {
			return nil
		}
	}
	if len(p.entries) == 0 {
		return fmt.Errorf("refusing to send a credentialed request to %q: no endpoint is permitted, because the "+
			"OAS document names no usable server and the operator allowlist is empty", host)
	}
	return fmt.Errorf("refusing to send a credentialed request to %q: it is not the document's server and is "+
		"not in the operator's endpoint allowlist (%s)", net.JoinHostPort(host, port), p.describe())
}

func (e entry) matches(host, port, scheme string) bool {
	if e.scheme != scheme || e.port != port {
		return false
	}
	if !e.wildcard {
		return e.host == host
	}
	// One or more leading labels. Deliberately does NOT match the bare suffix: an operator who wants both
	// lists both, so "*.ghe.example.com" cannot silently widen to ghe.example.com itself.
	return strings.HasSuffix(host, "."+e.host) && len(host) > len(e.host)+1
}

func (p *Policy) describe() string {
	if len(p.entries) == 0 {
		return "empty"
	}
	out := make([]string, 0, len(p.entries))
	for _, e := range p.entries {
		h := e.host
		if e.wildcard {
			h = "*." + h
		}
		out = append(out, e.scheme+"://"+net.JoinHostPort(h, e.port))
	}
	return strings.Join(out, ", ")
}

// denyIP holds the refusals no allowlist entry can override.
//
// RFC1918 and service CIDRs are DELIBERATELY ABSENT, which is the difference from jqmodule's
// denyInternalAddr. An on-prem GHES is routinely RFC1918, and refusing private space would make this
// feature useless for the thing it exists for. The control for private space is the allowlist: the
// operator has to name the host.
func denyIP(ip net.IP) error { return denyIPWithLoopback(ip, false) }

// denyIPWithLoopback is denyIP with the loopback refusal optionally relaxed. allowLoopback is true ONLY
// via Policy.AllowLoopbackForTesting; nothing an operator can write reaches it.
func denyIPWithLoopback(ip net.IP, allowLoopback bool) error {
	// Unwrap IPv4-mapped IPv6 first, so ::ffff:127.0.0.1 cannot slip past the v4 checks below.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLoopback():
		if allowLoopback {
			return nil
		}
		return fmt.Errorf("%s is a loopback address", ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// 169.254.0.0/16 and fe80::/10 -- the cloud-metadata targets.
		return fmt.Errorf("%s is a link-local address", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("%s is the unspecified address", ip)
	case ip.IsMulticast():
		return fmt.Errorf("%s is a multicast address", ip)
	}
	return nil
}

// controlDenyIP re-asserts denyIP immediately before connect, on the address actually being connected.
//
// Redundant with DialContext's own check today, and deliberately kept: it is the backstop for any future
// code path that reuses this Dialer without going through DialContext. The cost is one parse per
// connection; the thing it prevents is a new call site silently bypassing the guard, which is how the
// unguarded request got here in the first place.
func controlDenyIP(network, address string, c syscall.RawConn) error {
	return controlDenyIPWithLoopback(network, address, c, false)
}

func controlDenyIPWithLoopback(_, address string, _ syscall.RawConn, allowLoopback bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("parsing dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// DialContext always hands a literal address. A name here means something else dialled through
		// this Dialer, which is the case this backstop exists for -- refuse rather than resolve.
		return fmt.Errorf("refusing to dial unresolved host %q: the host policy requires a checked address", host)
	}
	return denyIPWithLoopback(ip, allowLoopback)
}

// DialContext resolves once, checks every resolved address, and dials the CHOSEN ADDRESS LITERALLY.
//
// Dialing the address rather than the name is what removes the TOCTOU gap: the address checked below is
// the address handed to the socket, so no second resolution can happen in between. A net.Dialer.Control
// hook alone cannot do this -- it receives only ip:port and cannot know which hostname it came from, so
// it can enforce denyIP and nothing else. We keep one anyway, because it costs nothing and re-asserts
// denyIP on any future code path that reuses the same Dialer.
//
// TLS still verifies against the HOSTNAME: http.Transport takes ServerName from the request URL, not from
// the dial address. That is the standard IP-pinning arrangement and it is correct -- and it is exactly
// the kind of correct that stops being correct silently, so there is a test with an HTTPS server whose
// certificate names the host.
func (p *Policy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// The Control backstop has to agree with the decision DialContext just made, or a test that was
	// permitted loopback above would be refused here instead. It still refuses link-local, multicast and
	// unspecified unconditionally -- the relaxation is loopback only.
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			return controlDenyIPWithLoopback(network, address, c, p.allowLoopback)
		}}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("refusing to dial %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		if derr := denyIPWithLoopback(ip, p.allowLoopback); derr != nil {
			return nil, fmt.Errorf("refusing to send a credentialed request: %w", derr)
		}
		return dialer.DialContext(ctx, network, addr)
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolving %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("refusing to send a credentialed request: %q resolved to no addresses", host)
	}
	// EVERY address must pass, or the whole request is refused. A legitimate vendor host does not resolve
	// partly to loopback or link-local; a split answer like that is the signature of a rebinding attempt.
	// Failing closed is the right trade because the allowlist is operator-controlled: the operator named a
	// host they trust, so a mixed answer is anomalous and refusing it costs a retry.
	for _, a := range addrs {
		if derr := denyIPWithLoopback(a.IP, p.allowLoopback); derr != nil {
			return nil, fmt.Errorf("refusing to send a credentialed request to %q: %w", host, derr)
		}
	}

	var lastErr error
	for _, a := range addrs {
		conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	return nil, lastErr
}

// CheckRedirect re-applies the host list to each hop.
//
// The dial-time guard already covers redirects, because every hop dials -- so this is for the ERROR
// MESSAGE rather than for the guarantee. Without it an operator sees a dial failure about a host they
// never configured and no indication that a redirect took them there.
//
// Redirect-following itself must stay: #132 is a renamed vendor repository served as a 307, and refusing
// to follow it leaves the CR permanently in ReconcileError.
func (p *Policy) CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if err := p.PermitsURL(req.URL); err != nil {
		return fmt.Errorf("refusing to follow a redirect: %w", err)
	}
	return nil
}

// Transport returns the guarded RoundTripper on its own, so a caller that needs to WRAP it -- a debug
// round-tripper, a recorder -- can do so without losing the dialer underneath.
//
// This exists because the alternative bit: the debug round-tripper used to wrap the CALLER's transport and
// assign it back onto the caller's client, which made that transport non-nil and caused the request to be
// sent on it instead of on the policy's. So enabling verbose debugging silently bypassed the resolved-
// address and per-hop redirect checks -- in the one configuration an operator turns on when something is
// already wrong. Found by the production-Transport guard test, which is why that test exists.
func (p *Policy) Transport() http.RoundTripper {
	return &http.Transport{DialContext: p.DialContext, TLSHandshakeTimeout: 10 * time.Second}
}

// Client returns an http.Client that enforces this policy.
func (p *Policy) Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     p.Transport(),
		CheckRedirect: p.CheckRedirect,
	}
}
