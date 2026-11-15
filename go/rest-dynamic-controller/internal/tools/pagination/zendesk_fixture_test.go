package pagination

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	getter "github.com/krateo-platformops/rest-dynamic-controller/internal/tools/definitiongetter"
)

// fixtureDir is acmp/zendesk-kog's collected fixture, committed under the controllers package.
// Read from there rather than copied here: a second copy would drift from the shapes they measured,
// and the point of the fixture is that its provenance is recorded next to it.
const fixtureDir = "../../controllers/testdata/zendesk-correlation"

func fixturePage(t *testing.T, name string) Page {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("reading the fixture %s: %v", name, err)
	}
	var env struct {
		Requests []interface{} `json:"requests"`
	}
	if jerr := json.Unmarshal(b, &env); jerr != nil {
		t.Fatalf("the fixture %s is not the expected envelope: %v", name, jerr)
	}
	return Page{Body: b, Items: env.Requests}
}

func zendeskCfg(hasMorePath string) *getter.ContinuationTokenConfig {
	return &getter.ContinuationTokenConfig{
		Request:  getter.ContinuationTokenRequest{TokenIn: "query", TokenPath: "page[after]"},
		Response: getter.ContinuationTokenResponse{TokenIn: "body", TokenPath: "meta.after_cursor", HasMorePath: hasMorePath},
	}
}

// TestZendeskFixtureTerminates is the fourth gap, against the real documented shapes.
//
// Zendesk documents ONE end signal -- repeat "until the meta[has_more] property is false" -- and does
// not document whether after_cursor is present, null or absent on the last page. acmp's fixture
// enumerates all three rather than guessing, so this table runs every one of them.
//
// The row that matters is cursor-PRESENT: under the token-absence rule that page says "more pages",
// so the walk never terminates and runs to the bound on every single correlation observe. With
// hasMorePath declared it terminates correctly.
func TestZendeskFixtureTerminates(t *testing.T) {
	for _, tc := range []struct {
		file           string
		withHasMore    PageResult
		absenceOnly    PageResult
		absenceIsWrong bool
	}{
		{file: "page2-final-cursor-absent.json", withHasMore: Exhausted, absenceOnly: Exhausted},
		{file: "page2-final-cursor-null.json", withHasMore: Exhausted, absenceOnly: Exhausted},
		{
			// The documented-unknown case that breaks the absence rule.
			file: "page2-final-cursor-present.json", withHasMore: Exhausted, absenceOnly: MorePages,
			absenceIsWrong: true,
		},
	} {
		t.Run(tc.file, func(t *testing.T) {
			page := fixturePage(t, tc.file)

			withHasMore := NewContinuationTokenPaginator(zendeskCfg("meta.has_more"))
			withHasMore.Init()
			got, err := withHasMore.Next(page)
			if err != nil || got != tc.withHasMore {
				t.Fatalf("with hasMorePath: verdict = %v, err = %v; want %v", got, err, tc.withHasMore)
			}

			absenceOnly := NewContinuationTokenPaginator(zendeskCfg(""))
			absenceOnly.Init()
			got2, err2 := absenceOnly.Next(page)
			if err2 != nil || got2 != tc.absenceOnly {
				t.Fatalf("absence rule only: verdict = %v, err = %v; want %v", got2, err2, tc.absenceOnly)
			}
			if tc.absenceIsWrong && got2 != MorePages {
				t.Fatal("this row exists to show the absence rule getting it WRONG; it did not")
			}
		})
	}
}

// TestZendeskFixtureFirstPageContinues is the setup control: if page 1 did not report MorePages, the
// final-page rows above would pass for the wrong reason -- a paginator that always says Exhausted.
func TestZendeskFixtureFirstPageContinues(t *testing.T) {
	page := fixturePage(t, "page1.json")
	for _, hasMore := range []string{"meta.has_more", ""} {
		p := NewContinuationTokenPaginator(zendeskCfg(hasMore))
		p.Init()
		got, err := p.Next(page)
		if err != nil || got != MorePages {
			t.Fatalf("hasMorePath=%q on page 1: verdict = %v, err = %v; want MorePages", hasMore, got, err)
		}
	}
}

// TestZendeskFixtureHasTheItemsTheCensusDescribes guards the fixture itself. If someone edits it, the
// tests above could keep passing while no longer exercising the shapes acmp measured.
func TestZendeskFixtureHasTheItemsTheCensusDescribes(t *testing.T) {
	p1 := fixturePage(t, "page1.json")
	p2 := fixturePage(t, "page2-final-cursor-present.json")
	if len(p1.Items) != 3 || len(p2.Items) != 2 {
		t.Fatalf("fixture item counts = %d and %d; want 3 and 2", len(p1.Items), len(p2.Items))
	}
}
