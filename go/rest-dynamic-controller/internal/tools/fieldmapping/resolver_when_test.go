package fieldmapping

import (
	"context"
	"strings"
	"testing"

	"encoding/base64"
	"github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic/fake"
)

// THE MOTIVATING CASE FOR #524, and every test here goes through ResolveRequestResolvers rather than
// calling resolverGuardSkips directly.
//
// That is deliberate. #510's first implementation had a collector, a lister, a sync and a teardown, each
// unit-tested and green, and provisioned NOTHING on a real cluster -- because nothing called them in the
// normal flow. A mutant removing both call sites survived the entire suite. So the question these tests
// have to answer is not "does the guard work" but "is the guard reached".
//
// A KeycloakUser with a password credential and NO otp credential: before #524 the OTP mapping's path was
// unresolvable, so the create hard-errored and the user could never exist (keycloak-operator-kog#20).
func keycloakUserWith(t *testing.T, creds []any) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"credentials": creds},
	}}
	u.SetNamespace("ns1")
	u.SetName("tester")
	return u
}

func passwordCred(secretName string) any {
	return map[string]any{
		"type":           "password",
		"valueSecretRef": map[string]any{"name": secretName, "key": "password"},
	}
}

func otpCredWithRef(secretName string) any {
	return map[string]any{
		"type":                "otp",
		"secretDataSecretRef": map[string]any{"name": secretName, "key": "seed"},
	}
}

// Two mappings, as the real chart declares them: one per credential type, each guarded on its own entry.
func guardedCredentialMappings() []getter.FieldMappingItem {
	return []getter.FieldMappingItem{
		{
			InBody:           "credentials[?type=password].value",
			InCustomResource: "spec.credentials[?type=password].valueSecretRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				When: &getter.ResolverWhen{CustomResourceFieldPresent: "spec.credentials[?type=password]"},
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentials[?type=password].valueSecretRef.name",
					KeyFromCustomResource:  "spec.credentials[?type=password].valueSecretRef.key",
				},
			},
		},
		{
			InBody:           "credentials[?type=otp].secretData",
			InCustomResource: "spec.credentials[?type=otp].secretDataSecretRef",
			Resolver: &getter.FieldResolver{
				Type: "secretRef",
				When: &getter.ResolverWhen{CustomResourceFieldPresent: "spec.credentials[?type=otp]"},
				SecretRef: &getter.SecretRefResolver{
					NameFromCustomResource: "spec.credentials[?type=otp].secretDataSecretRef.name",
					KeyFromCustomResource:  "spec.credentials[?type=otp].secretDataSecretRef.key",
				},
			},
		},
	}
}

// CASE 1 of the consumer's matrix: password-only. Before #524 this was an unconditional hard error.
func TestAPasswordOnlyUserResolvesItsPasswordAndSkipsTheGuardedOtpMapping(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "gate1-test-users", "password", "hunter2")
	mg := keycloakUserWith(t, []any{passwordCred("gate1-test-users")})
	mapping := guardedCredentialMappings()

	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("a password-only user must resolve, not error: %v", err)
	}
	if got := resolved[ResolverKey(mapping[0])]; got != "hunter2" {
		t.Fatalf("the password mapping must still resolve; got %v", got)
	}
	if _, present := resolved[ResolverKey(mapping[1])]; present {
		t.Fatal("the OTP mapping must be SKIPPED, contributing nothing to the request")
	}
}

// addSecret puts a second Secret into an existing fake client. The package's newFakeClientWithSecret
// creates one Secret and returns the client; case 2 needs two.
func addSecret(t *testing.T, dyn *fake.FakeDynamicClient, ns, name, key, value string) {
	t.Helper()
	sec := &unstructured.Unstructured{}
	sec.SetAPIVersion("v1")
	sec.SetKind("Secret")
	sec.SetNamespace(ns)
	sec.SetName(name)
	data := map[string]interface{}{key: base64.StdEncoding.EncodeToString([]byte(value))}
	if err := unstructured.SetNestedMap(sec.Object, data, "data"); err != nil {
		t.Fatalf("SetNestedMap: %v", err)
	}
	if _, err := dyn.Resource(secretGVR).Namespace(ns).Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating secret %s/%s: %v", ns, name, err)
	}
}

// CASE 2: both credentials present. Both resolve -- the guard must not suppress a mapping that applies.
func TestAUserWithBothCredentialsResolvesBoth(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "pw-secret", "password", "hunter2")
	addSecret(t, dyn, "ns1", "otp-secret", "seed", "JBSWY3DP")
	mg := keycloakUserWith(t, []any{passwordCred("pw-secret"), otpCredWithRef("otp-secret")})
	mapping := guardedCredentialMappings()

	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resolved[ResolverKey(mapping[0])]; got != "hunter2" {
		t.Fatalf("password: got %v", got)
	}
	if got := resolved[ResolverKey(mapping[1])]; got != "JBSWY3DP" {
		t.Fatalf("otp: got %v -- a guard whose path RESOLVES must not skip", got)
	}
}

// CASE 3a: the entry is present and its ref is absent. The guard is SATISFIED, so the resolver runs and
// refuses loudly. This is the distinction the whole design turns on: a present entry with a missing ref is
// an author error, not an absence.
func TestAnOtpEntryPresentWithNoRefStillErrors(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "pw-secret", "password", "hunter2")
	mg := keycloakUserWith(t, []any{
		passwordCred("pw-secret"),
		map[string]any{"type": "otp"}, // declared, no secretDataSecretRef
	})

	_, err := ResolveRequestResolvers(context.Background(), dyn, guardedCredentialMappings(), mg, nil)
	if err == nil {
		t.Fatal("an OTP entry with no ref must be refused: the guard is satisfied, so the ref is required")
	}
	if strings.Contains(err.Error(), "when.customResourceFieldPresent") {
		t.Fatalf("the error must come from the REF, not from the guard (the guard resolved fine): %v", err)
	}
}

// CASE 3b: an explicitly null entry counts as absent, matching guardSkips' existing convention.
func TestAnExplicitlyNullEntryIsTreatedAsAbsent(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "pw-secret", "password", "hunter2")
	mg := keycloakUserWith(t, []any{passwordCred("pw-secret")})
	// The guard path points at something explicitly null rather than missing.
	mapping := guardedCredentialMappings()
	mapping[1].Resolver.When.CustomResourceFieldPresent = "spec.nilField"
	mg.Object["spec"].(map[string]any)["nilField"] = nil

	resolved, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil)
	if err != nil {
		t.Fatalf("an explicit null must skip, not error: %v", err)
	}
	if _, present := resolved[ResolverKey(mapping[1])]; present {
		t.Fatal("an explicitly null guard path must skip the mapping")
	}
}

// CASE 4: two `type: otp` entries make the GUARD path ambiguous. ERROR, not skip -- a consumer's argument:
// a skip would turn a malformed user into a password-only one, which is the outcome the guard prevents.
func TestAnAmbiguousGuardPathIsAnErrorRatherThanASkip(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "pw-secret", "password", "hunter2")
	mg := keycloakUserWith(t, []any{
		passwordCred("pw-secret"),
		otpCredWithRef("otp-a"),
		otpCredWithRef("otp-b"),
	})

	_, err := ResolveRequestResolvers(context.Background(), dyn, guardedCredentialMappings(), mg, nil)
	if err == nil {
		t.Fatal("two matching entries make the guard ambiguous; that must be an error, because a skip " +
			"would silently reconcile the user WITHOUT the credential the guard exists to protect")
	}
	if !strings.Contains(err.Error(), "when.customResourceFieldPresent") {
		t.Fatalf("the error must name the guard, so an operator knows which path was ambiguous: %v", err)
	}
}

// THE CONTROL FOR EVERY TEST ABOVE: with NO guard, behaviour is exactly what it was before #524 -- an
// unconditional hard error on a password-only user. Without this, a guard that skipped everything would
// pass case 1 and look correct.
func TestWithoutAGuardAPasswordOnlyUserStillHardErrors(t *testing.T) {
	dyn := newFakeClientWithSecret(t, "ns1", "gate1-test-users", "password", "hunter2")
	mg := keycloakUserWith(t, []any{passwordCred("gate1-test-users")})
	mapping := guardedCredentialMappings()
	mapping[0].Resolver.When = nil
	mapping[1].Resolver.When = nil

	if _, err := ResolveRequestResolvers(context.Background(), dyn, mapping, mg, nil); err == nil {
		t.Fatal("with no guard the OTP mapping is unconditional, so a password-only user must still fail: " +
			"this is the behaviour every existing definition relies on")
	}
}
