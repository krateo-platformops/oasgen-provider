package restclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// THE pageNumber STRATEGY, THROUGH THE SHARED WALK.
//
// The two existing arm tests both use continuationToken, so they share a blind spot that
// github-provider-kog pointed out: every live regression suite named for #710's refactor is
// continuationToken, because azuredevops-provider-kog has 0 pageNumber kinds. github has 3 live
// pageNumber findby kinds (PullRequest, Collaborator, UserRepository) and PullRequest findby past page
// one was measured live on 2026-11-06.
//
// So the pageNumber arms of WalkPaginated had no coverage at all -- not in a suite, and not here. A
// live re-run needs their user's approval in their session, which is not mine to grant or ask for;
// this is the part that can be done without anyone's cluster, and it runs on every pull request.
//
// It drives the SHORT-PAGE rule (no response block) rather than a token, so the end-of-collection
// decision comes from a different mechanism than the continuationToken tests exercise.

func pageNumberServer(t *testing.T, pages [][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			page = n
		}
		idx := page - 1
		items := []string{}
		if idx >= 0 && idx < len(pages) {
			items = pages[idx]
		}
		body := `{"items": [`
		for i, name := range items {
			if i > 0 {
				body += ","
			}
			body += fmt.Sprintf(`{"id": "%s", "name": "%s"}`, name, name)
		}
		body += `]}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, body)
	}))
}

// pageSize is 2, so a page with fewer than 2 items is the last one -- the short-page rule.
func pageNumberVerb(maxPages int) *getter.VerbsDescription {
	return &getter.VerbsDescription{
		Action: "findby",
		Pagination: &getter.Pagination{
			Type: "pageNumber",
			PageNumber: &getter.PageNumberConfig{
				Request: getter.PageNumberRequest{
					PageIn: "query", PagePath: "page", StartPage: 1,
					SizeIn: "query", SizePath: "size", PageSize: 2,
				},
				MaxPages: maxPages,
			},
		},
	}
}

// TestWalkPaginatedPageNumberFindsOnALaterPage is the case github measured live: the match is not on
// page one, so a walk that stopped there would report absence on a resource that exists -- and absence
// is what the reconciler acts on by CREATING.
func TestWalkPaginatedPageNumberFindsOnALaterPage(t *testing.T) {
	server := pageNumberServer(t, [][]string{{"one", "two"}, {"three"}})
	defer server.Close()

	client := pagedClient(t, server.URL, "three")

	resp, err := client.FindBy(context.Background(), server.Client(), "/items",
		&RequestConfiguration{Method: http.MethodGet}, pageNumberVerb(10))
	if err != nil {
		t.Fatalf("the match is on page 2 and should have been found: %v", err)
	}
	body, ok := resp.ResponseBody.(map[string]interface{})
	if !ok {
		t.Fatalf("want an object body, got %T", resp.ResponseBody)
	}
	if body["name"] != "three" {
		t.Errorf("matched %v, want \"three\" -- the item on page 2", body["name"])
	}
}

// TestWalkPaginatedPageNumberExhaustedIsASynthesisedNotFound is the pageNumber twin of the
// continuationToken arm test. The short-page rule ends the collection; a complete scan with no match
// must be a Synthesised 404 so the existence checker can tell OUR 404 from the vendor's (#371).
func TestWalkPaginatedPageNumberExhaustedIsASynthesisedNotFound(t *testing.T) {
	server := pageNumberServer(t, [][]string{{"one", "two"}, {"three"}})
	defer server.Close()

	client := pagedClient(t, server.URL, "absent")

	_, err := client.FindBy(context.Background(), server.Client(), "/items",
		&RequestConfiguration{Method: http.MethodGet}, pageNumberVerb(10))
	if err == nil {
		t.Fatal("a complete pageNumber scan with no match must report not-found, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("want a *StatusError the existence checker can read, got %T: %v", err, err)
	}
	if se.StatusCode != http.StatusNotFound || !se.Synthesised {
		t.Errorf("StatusCode=%d Synthesised=%v; want 404 and Synthesised", se.StatusCode, se.Synthesised)
	}
}

// TestWalkPaginatedPageNumberBoundIsNotAbsence pins the rule that matters most on this strategy: the
// author's maxPages is a bound on LOOKING, not a conclusion about existence. Reaching it must not
// produce a 404, because the reconciler creates on 404 -- so collapsing the two duplicates a resource
// the walk never finished searching for.
func TestWalkPaginatedPageNumberBoundIsNotAbsence(t *testing.T) {
	// Every page is full, so the short-page rule never fires and only the bound can stop the walk.
	full := [][]string{{"a", "b"}, {"c", "d"}, {"e", "f"}, {"g", "h"}}
	server := pageNumberServer(t, full)
	defer server.Close()

	client := pagedClient(t, server.URL, "absent")

	_, err := client.FindBy(context.Background(), server.Client(), "/items",
		&RequestConfiguration{Method: http.MethodGet}, pageNumberVerb(2))
	if err == nil {
		t.Fatal("reaching maxPages must be reported, got nil")
	}
	var se *StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		t.Fatalf(`reaching maxPages produced a 404: %v

That is the collapse this package exists to prevent. "I was told to stop looking" is not "it is not
there", and the reconciler CREATES on 404 -- so a bounded walk reporting absence duplicates a resource
it never finished searching for.`, err)
	}
	if !strings.Contains(err.Error(), "maxPages") {
		t.Errorf("the error should name the bound so an operator knows which knob to turn, got %v", err)
	}
}

// TestWalkPaginatedHardBackstopIsNotAbsence covers pagination.MaxFindByPages, the 1000-page runaway
// backstop -- which NOTHING covered until now.
//
// Found by mutation: making the backstop return a Synthesised 404 broke no test. The pageNumber bound
// test above exercises the PER-STRATEGY maxPages in page_number.go, a different bound in a different
// file, so it passed while the backstop was collapsed.
//
// AND THE FIRST VERSION OF THIS TEST COULD NOT REACH IT EITHER, which is a finding in itself. Driving
// it with pageNumber and maxPages=1000 trips the strategy bound instead: the loop checks the backstop
// BEFORE fetching and the strategy bound AFTER, so at equal values the strategy one fires first -- and
// NewPaginator refuses a pageNumber maxPages ABOVE MaxFindByPages. So for pageNumber the backstop is
// unreachable by construction; the strategy bound always gets there first or together.
//
// It is reachable exactly where the code says it is: a continuationToken walk with NO author maxPages,
// where 0 means unbounded and this constant is the only cap. That is also the live shape --
// azuredevops-provider-kog has four continuationToken findby kinds that set no bound -- so this is the
// one combination in which a 404 from the backstop would duplicate a real resource.
func TestWalkPaginatedHardBackstopIsNotAbsence(t *testing.T) {
	// A server that NEVER stops sending a next-token: the absence rule can never fire, and with no
	// author bound the backstop is the only thing that can end this walk.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Next-Token", "always-another-page")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"items": [{"id": "x", "name": "x"}]}`)
	}))
	defer server.Close()

	client := pagedClient(t, server.URL, "absent")

	verb := &getter.VerbsDescription{
		Action: "findby",
		Pagination: &getter.Pagination{
			Type: "continuationToken",
			ContinuationToken: &getter.ContinuationTokenConfig{
				Request:  getter.ContinuationTokenRequest{TokenIn: "query", TokenPath: "token"},
				Response: getter.ContinuationTokenResponse{TokenIn: "header", TokenPath: "X-Next-Token"},
				// MaxPages deliberately unset: 0 means no author bound, which is what every
				// pre-0.73.0 continuationToken RestDefinition carries.
			},
		},
	}

	_, err := client.FindBy(context.Background(), server.Client(), "/items",
		&RequestConfiguration{Method: http.MethodGet}, verb)
	if err == nil {
		t.Fatal("a never-terminating cursor must be stopped by the backstop, got nil")
	}

	var se *StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		t.Fatalf(`the 1000-page backstop produced a 404: %v

IsNotFoundError keys on a 404 StatusError and the reconciler acts on not-found by CREATING, so this
would say "the resource does not exist" when the truth is "I stopped looking". A continuationToken
walk with no author maxPages is bounded HERE AND NOWHERE ELSE, and four live kinds are that shape.`, err)
	}
	if !strings.Contains(err.Error(), "safety limit") {
		t.Errorf("the error should say it is a safety limit rather than a conclusion, got %v", err)
	}
}
