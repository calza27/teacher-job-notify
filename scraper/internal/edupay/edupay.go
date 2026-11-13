// Package edupay searches the Victorian Department of Education's job board
// (EduPay), which runs on PeopleSoft Candidate Gateway.
//
// The search page is a single HTML form. Selecting a filter ("facet") is a
// PeopleSoft postback: the browser POSTs the whole form with
// ICAction=<control id>, and the server replies with the re-rendered page.
// That's why search criteria never appear in the URL. The server keeps the
// search state in the session, so each search uses its own cookie jar.
package edupay

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// SearchURL is the public job search page.
const SearchURL = "https://edupay.eduweb.vic.gov.au/psc/EDUPPRD1_EA/APPLICANT/HRMS/c/HRS_HRAM_FL.HRS_CG_SEARCH_FL.GBL?Page=HRS_APP_SCHJOB_FL&Action=U"

// PostingURLFormat is the direct link to one job posting, as used by the
// site's own "Share" button. Arguments: job opening ID, posting sequence.
const PostingURLFormat = "https://edupay.eduweb.vic.gov.au/psc/EDUPPRD1_EA/APPLICANT/HRMS/c/HRS_HRAM_FL.HRS_CG_SEARCH_FL.GBL?Page=HRS_APP_JBPST_FL&Action=U&FOCUS=Applicant&SiteId=1&JobOpeningId=%s&PostingSeq=%d"

// The site renders at most this many results before "Show More".
const pageSize = 300

// How many posting sequence numbers to try when building a job's link.
// The search results don't include the sequence. Most jobs are 1; jobs that
// have been re-advertised are 2 or more.
const maxPostingSeq = 10

// Element ID prefixes for each field in a result row. PeopleSoft suffixes
// each with "$<row index>", e.g. SCH_JOB_TITLE$0.
const (
	fieldID      = "HRS_APP_JBSCH_I_HRS_JOB_OPENING_ID"
	fieldTitle   = "SCH_JOB_TITLE"
	fieldSchool  = "HRS_APP_JBSCH_I_HRS_DEPT_DESCR"
	fieldClosing = "HRS_CLS_DT_DESCR"
	fieldHours   = "DOE_APP_JBSCH_I_STD_HOURS"
	fieldType    = "DOE_APP_JBSCH_I_REG_TEMP"
)

// Job is one row from the search results.
type Job struct {
	ID          string
	Title       string
	School      string
	ClosingDate time.Time // zero if the site's value couldn't be parsed
	Hours       string    // weekly standard hours, e.g. "38.00"
	Type        string    // "Ongoing" or "Fixed Term"
	URL         string    // direct link to the posting; set by PostingURL, not by searches
}

// FacetGroup selects values in one of the search page's filter groups.
// The site ORs values within a group and ANDs separate groups.
type FacetGroup struct {
	Group  string   // group heading, e.g. "Locality"
	Values []string // value names without the count, e.g. "Casey"
}

// Client searches the job board.
type Client struct {
	URL              string
	PostingURLFormat string
	UserAgent        string
	Timeout          time.Duration // per request
}

// NewClient returns a Client with sensible defaults.
func NewClient() *Client {
	return &Client{
		URL:              SearchURL,
		PostingURLFormat: PostingURLFormat,
		UserAgent:        "teacher-job-notify/0.1 (personal job alert)",
		Timeout:          30 * time.Second,
	}
}

// session returns an HTTP client with its own cookie jar. The site sets
// session cookies on a redirect and expects them back; without a jar it
// serves an error page.
func (c *Client) session() *http.Client {
	jar, _ := cookiejar.New(nil) // only errors on non-nil options
	return &http.Client{Timeout: c.Timeout, Jar: jar}
}

// PostingURL returns a direct link to a job's posting page. It tries each
// posting sequence number in turn, because a wrong one silently shows the
// search page instead. It returns "" if none of them open the posting.
func (c *Client) PostingURL(ctx context.Context, jobID string) (string, error) {
	for seq := 1; seq <= maxPostingSeq; seq++ {
		link := fmt.Sprintf(c.PostingURLFormat, url.QueryEscape(jobID), seq)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return "", err
		}
		pg, err := c.do(c.session(), req)
		if err != nil {
			return "", fmt.Errorf("checking posting link for job %s: %w", jobID, err)
		}
		if pg.isPosting(jobID) {
			return link, nil
		}
	}
	return "", nil
}

// Search loads the search page in a fresh session, applies each facet group
// in order, and returns the jobs that match. If none of a group's values are
// on offer (the site hides values with zero results), nothing matches and
// Search returns no jobs.
func (c *Client) Search(ctx context.Context, groups []FacetGroup) ([]Job, error) {
	hc := c.session()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, err
	}
	pg, err := c.do(hc, req)
	if err != nil {
		return nil, fmt.Errorf("loading search page: %w", err)
	}

	for _, g := range groups {
		applied := 0
		for _, v := range g.Values {
			id, ok := pg.facetControl(g.Group, v)
			if !ok {
				continue
			}
			if pg, err = c.postback(ctx, hc, pg, id); err != nil {
				return nil, fmt.Errorf("applying filter %s=%s: %w", g.Group, v, err)
			}
			applied++
		}
		if applied == 0 {
			return nil, nil
		}
	}

	jobs := pg.jobs()
	if len(jobs) >= pageSize {
		slog.Warn("search returned a full page; results beyond it are not fetched", "jobs", len(jobs))
	}
	return jobs, nil
}

// Parse extracts jobs from a search results page.
func Parse(r io.Reader) ([]Job, error) {
	pg, err := newPage(r, nil)
	if err != nil {
		return nil, err
	}
	return pg.jobs(), nil
}

type page struct {
	doc *goquery.Document
	url *url.URL // where the page was served from, for resolving the form action
}

func newPage(r io.Reader, u *url.URL) (*page, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}
	if doc.Find("form[name='win0']").Length() == 0 {
		title := strings.TrimSpace(doc.Find("title").Text())
		return nil, fmt.Errorf("not the search page (title %q): the site may be down or its layout changed", title)
	}
	return &page{doc: doc, url: u}, nil
}

func (c *Client) do(hc *http.Client, req *http.Request) (*page, error) {
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return newPage(resp.Body, resp.Request.URL)
}

// postback submits the page's form as if the control with the given ID
// (a facet checkbox) had been clicked.
func (c *Client) postback(ctx context.Context, hc *http.Client, pg *page, controlID string) (*page, error) {
	form := pg.doc.Find("form[name='win0']")
	action, err := pg.url.Parse(form.AttrOr("action", ""))
	if err != nil {
		return nil, fmt.Errorf("form action: %w", err)
	}

	values := url.Values{}
	form.Find("input[type='hidden'][name]").Each(func(_ int, s *goquery.Selection) {
		values.Set(s.AttrOr("name", ""), s.AttrOr("value", ""))
	})
	values.Set("ICAction", controlID)
	values.Set(controlID, "Y")
	// PeopleSoft pairs each checkbox with a hidden PTS_SELECT$chk$<n> input.
	values.Set(strings.Replace(controlID, "$", "$chk$", 1), "Y")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, action.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(hc, req)
}

// isPosting reports whether the page is the posting page for jobID. The
// search page and the posting page share a form; only its label differs.
func (pg *page) isPosting(jobID string) bool {
	label := pg.doc.Find("form[name='win0']").AttrOr("aria-label", "")
	return label == "Job Description" && strings.Contains(pg.doc.Text(), jobID)
}

var facetLabel = regexp.MustCompile(`^(.*?)\s*\(\d+\)$`)

// facetControl returns the checkbox ID for a value within a facet group.
// Values are looked up within their group because some names, such as
// "Leading Teacher", appear in more than one group.
func (pg *page) facetControl(group, value string) (string, bool) {
	var id string
	pg.doc.Find("[id^='win0divPTS_FACETS_row$']").EachWithBreak(func(_ int, row *goquery.Selection) bool {
		heading := normalise(row.Find("[id^='PTS_FACET_PTS_GROUP_BOX$']").First().Text())
		if !strings.EqualFold(heading, group) {
			return true
		}
		row.Find("label[id^='PTS_SELECT_LBL$']").EachWithBreak(func(_ int, l *goquery.Selection) bool {
			m := facetLabel.FindStringSubmatch(normalise(l.Text()))
			if m != nil && strings.EqualFold(m[1], value) {
				id = l.AttrOr("for", "")
				return false
			}
			return true
		})
		return false
	})
	return id, id != ""
}

func (pg *page) jobs() []Job {
	wanted := map[string]bool{
		fieldID: true, fieldTitle: true, fieldSchool: true,
		fieldClosing: true, fieldHours: true, fieldType: true,
	}

	// One pass over the DOM: rows[index][field] = text.
	rows := map[string]map[string]string{}
	var order []string
	pg.doc.Find("[id*='$']").Each(func(_ int, s *goquery.Selection) {
		field, idx, ok := strings.Cut(s.AttrOr("id", ""), "$")
		if !ok || !wanted[field] || strings.Contains(idx, "$") {
			return
		}
		if rows[idx] == nil {
			rows[idx] = map[string]string{}
			order = append(order, idx)
		}
		rows[idx][field] = normalise(s.Text())
	})

	jobs := make([]Job, 0, len(order))
	for _, idx := range order {
		row := rows[idx]
		if row[fieldID] == "" {
			continue
		}
		job := Job{
			ID:     row[fieldID],
			Title:  row[fieldTitle],
			School: row[fieldSchool],
			Hours:  row[fieldHours],
			Type:   row[fieldType],
		}
		// The site renders closing dates as MM/DD/YYYY.
		if t, err := time.Parse("01/02/2006", row[fieldClosing]); err == nil {
			job.ClosingDate = t
		}
		jobs = append(jobs, job)
	}
	return jobs
}

// normalise trims and collapses runs of whitespace.
func normalise(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
