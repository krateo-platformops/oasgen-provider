//go:build unit || integration

package restclient

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRedactHidesCredentialHeadersNobodyRegistered is a security regression test.
//
// Value-based redaction can only hide values it was told about, and SensitiveValues is populated solely
// from secretRef-resolved fields. A bearer token supplied through the resource's own Configuration is
// applied straight to the request by SetAuth and never registered — so with krateo.io/connector-verbose
// on, `Authorization: Bearer <token>` went to pod logs in cleartext. Reported from a live cluster.
//
// A header whose job is to carry a credential does not need to be recognised by value to be known
// dangerous, so these are redacted by NAME.
func TestRedactHidesCredentialHeadersNobodyRegistered(t *testing.T) {
	const token = "ghp_averysecrettokenvalue"
	dump := "GET /repos/o/r HTTP/1.1\r\n" +
		"Host: api.github.com\r\n" +
		"Authorization: Bearer " + token + "\r\n" +
		"Accept: application/json\r\n\r\n"

	// Nothing registered as sensitive — the case that leaked.
	got := string(redact([]byte(dump), nil))

	assert.NotContains(t, got, token,
		"the credential reached the log in cleartext; it was never registered as sensitive, which is "+
			"exactly why redaction must key on the header name rather than on known values")
	assert.Contains(t, got, "Authorization: ***REDACTED***")
	assert.Contains(t, got, "Accept: application/json", "non-credential headers must survive intact")
	assert.Contains(t, got, "Host: api.github.com")
}

func TestRedactCoversTheOtherCredentialHeaders(t *testing.T) {
	for _, h := range []string{"Proxy-Authorization", "Cookie", "X-Api-Key", "X-Auth-Token", "Private-Token"} {
		t.Run(h, func(t *testing.T) {
			dump := "GET / HTTP/1.1\r\n" + h + ": supersecret\r\n\r\n"
			got := string(redact([]byte(dump), nil))
			assert.NotContains(t, got, "supersecret")
			assert.Contains(t, got, h+": ***REDACTED***")
		})
	}
}

// Case-insensitive, because Go canonicalises header names but a dump may not.
func TestRedactIsCaseInsensitive(t *testing.T) {
	got := string(redact([]byte("GET / HTTP/1.1\r\nauthorization: Bearer leaky\r\n\r\n"), nil))
	assert.NotContains(t, got, "leaky")
}

// Value-based redaction must keep working: it covers secrets that appear in a BODY, which no header
// rule can reach.
func TestRedactStillHidesRegisteredValuesInBodies(t *testing.T) {
	dump := "POST / HTTP/1.1\r\n\r\n{\"password\":\"s3cr3t\"}"
	got := string(redact([]byte(dump), []string{"s3cr3t"}))
	assert.NotContains(t, got, "s3cr3t")
	assert.True(t, strings.Contains(got, "***REDACTED***"))
}
