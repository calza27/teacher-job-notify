package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/calza27/teacher-job-notify/scraper/internal/edupay"
)

var sections = []Section{
	{Heading: "Full time", Jobs: []edupay.Job{
		{ID: "1", Title: "Classroom Teacher F-6", School: "Clyde Creek Primary School", Hours: "38.00", Type: "Ongoing",
			ClosingDate: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), URL: "https://example.test/job/1"},
		{ID: "2", Title: "Graduate Teacher", School: "Officer Primary School", Hours: "38.00", Type: "Fixed Term"},
	}},
	{Heading: "Part time", Jobs: nil},
}

func TestSubject(t *testing.T) {
	if got, want := Subject(sections), "2 new jobs: 2 full time"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	both := append(sections[:1:1], Section{Heading: "Part time", Jobs: []edupay.Job{{ID: "3"}}})
	if got, want := Subject(both), "3 new jobs: 2 full time, 1 part time"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
}

func TestBody(t *testing.T) {
	body := Body(sections)
	for _, want := range []string{
		"FULL TIME (2)\n=============",
		"[ONGOING] Classroom Teacher F-6\n    Clyde Creek Primary School\n    38.00 hrs/week | closes Thu 26 Nov 2026 | Job ID 1\n    https://example.test/job/1\n",
		// Job 2 has no URL, so it points at the search page instead.
		"[FIXED TERM] Graduate Teacher\n    Officer Primary School\n    38.00 hrs/week | Job ID 2\n    (no direct link",
		edupay.SearchURL,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "PART TIME") {
		t.Errorf("body includes an empty section:\n%s", body)
	}
}

func TestBodyAllLinkedOmitsSearchPage(t *testing.T) {
	body := Body([]Section{{Heading: "Full time", Jobs: []edupay.Job{{ID: "1", URL: "https://example.test/job/1"}}}})
	if strings.Contains(body, edupay.SearchURL) {
		t.Errorf("body includes the search page although every job has a link:\n%s", body)
	}
}
