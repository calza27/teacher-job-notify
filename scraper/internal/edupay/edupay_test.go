package edupay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestParse(t *testing.T) {
	jobs, err := Parse(openFixture(t, "results.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}

	want := Job{
		ID:          "1553326",
		Title:       "Classroom Teacher F-6",
		School:      "Clyde Creek Primary School",
		ClosingDate: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		Hours:       "38.00",
		Type:        "Fixed Term",
	}
	if jobs[0] != want {
		t.Errorf("jobs[0] = %+v\nwant      %+v", jobs[0], want)
	}
	if jobs[1].ID != "1553046" || jobs[1].Type != "Ongoing" {
		t.Errorf("jobs[1] = %+v", jobs[1])
	}
}

func TestParseErrorPage(t *testing.T) {
	if _, err := Parse(openFixture(t, "error.html")); err == nil {
		t.Fatal("expected an error for a page without the search form")
	}
}

// fakeSite serves the results fixture for every request and records the
// ICAction of each postback.
func fakeSite(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	body, err := os.ReadFile("testdata/results.html")
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			a := r.PostForm.Get("ICAction")
			actions = append(actions, a)
			if r.PostForm.Get("ICSID") != "abc123" {
				t.Errorf("postback %s missing hidden form state", a)
			}
			if chk := strings.Replace(a, "$", "$chk$", 1); r.PostForm.Get(chk) != "Y" {
				t.Errorf("postback %s missing %s=Y", a, chk)
			}
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &actions
}

func TestSearchAppliesFacetsInGroup(t *testing.T) {
	srv, actions := fakeSite(t)
	c := NewClient()
	c.URL = srv.URL + "/search"

	jobs, err := c.Search(context.Background(), []FacetGroup{
		{Group: "Locality", Values: []string{"Casey", "Cardinia", "Nowhere"}},
		{Group: "Role Type", Values: []string{"Leading Teacher"}},
		{Group: "Full/Part-Time", Values: []string{"part-time"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Errorf("got %d jobs, want 2", len(jobs))
	}

	// "Leading Teacher" is also in Job Family ($3); the Role Type one is $4.
	// "Nowhere" isn't offered, so it's skipped.
	want := []string{"PTS_SELECT$10", "PTS_SELECT$11", "PTS_SELECT$4", "PTS_SELECT$21"}
	if !slices.Equal(*actions, want) {
		t.Errorf("postbacks = %v, want %v", *actions, want)
	}
}

func TestPostingURL(t *testing.T) {
	posting, err := os.ReadFile("testdata/posting.html")
	if err != nil {
		t.Fatal(err)
	}
	search, err := os.ReadFile("testdata/results.html")
	if err != nil {
		t.Fatal(err)
	}
	// Like the real site: the right sequence number shows the posting, a
	// wrong one falls back to the search page.
	var tried []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tried = append(tried, q.Get("seq"))
		if q.Get("id") == "1553326" && q.Get("seq") == "2" {
			w.Write(posting)
			return
		}
		w.Write(search)
	}))
	t.Cleanup(srv.Close)

	c := NewClient()
	c.PostingURLFormat = srv.URL + "/posting?id=%s&seq=%d"

	got, err := c.PostingURL(context.Background(), "1553326")
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/posting?id=1553326&seq=2"; got != want {
		t.Errorf("PostingURL = %q, want %q", got, want)
	}
	if !slices.Equal(tried, []string{"1", "2"}) {
		t.Errorf("tried sequences %v, want [1 2]", tried)
	}

	// The fixture posting is for 1553326, so another ID never matches.
	tried = nil
	got, err = c.PostingURL(context.Background(), "999")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("PostingURL for unknown job = %q, want empty", got)
	}
	if len(tried) != maxPostingSeq {
		t.Errorf("tried %d sequences, want %d", len(tried), maxPostingSeq)
	}
}

func TestSearchNoMatchingFacetReturnsNothing(t *testing.T) {
	srv, actions := fakeSite(t)
	c := NewClient()
	c.URL = srv.URL + "/search"

	jobs, err := c.Search(context.Background(), []FacetGroup{
		{Group: "Locality", Values: []string{"Nowhere"}},
		{Group: "Full/Part-Time", Values: []string{"Full-Time"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want none when a filter value isn't offered", len(jobs))
	}
	if len(*actions) != 0 {
		t.Errorf("postbacks = %v, want none", *actions)
	}
}
