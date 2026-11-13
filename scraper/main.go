// Command scraper checks EduPay for new primary teaching jobs in Casey and
// Cardinia and emails an alert for any it hasn't notified before.
//
// In Lambda it runs once per invocation (on a daily schedule). Run it
// locally with -local to print what it finds without touching AWS:
//
//	go run . -local
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/calza27/teacher-job-notify/scraper/internal/edupay"
	"github.com/calza27/teacher-job-notify/scraper/internal/notify"
	"github.com/calza27/teacher-job-notify/scraper/internal/store"
)

// search is one query against the job board; its results are listed under
// Heading in the email.
type search struct {
	Heading string
	Facets  []edupay.FacetGroup
}

// Filter groups and values must match the site's labels exactly (ignoring
// case and the counts in brackets).
var (
	primaryInCaseyCardinia = []edupay.FacetGroup{
		{Group: "Locality", Values: []string{"Casey", "Cardinia"}},
		{Group: "Role Type", Values: []string{"Primary Teacher"}},
	}
	searches = []search{
		{Heading: "Full time", Facets: slices.Concat(primaryInCaseyCardinia, []edupay.FacetGroup{
			{Group: "Full/Part-Time", Values: []string{"Full-Time"}},
		})},
		{Heading: "Part time", Facets: slices.Concat(primaryInCaseyCardinia, []edupay.FacetGroup{
			{Group: "Full/Part-Time", Values: []string{"Part-Time"}},
		})},
	}
)

type app struct {
	client   *edupay.Client
	store    *store.Store
	notifier *notify.SNS
}

func main() {
	local := flag.Bool("local", false, "print current matching jobs without using AWS")
	flag.Parse()

	if *local {
		ctx := context.Background()
		client := edupay.NewClient()
		sections, err := runSearches(ctx, client)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		addLinks(ctx, client, sections)
		fmt.Printf("Subject: %s\n\n%s", notify.Subject(sections), notify.Body(sections))
		return
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		slog.Error("loading AWS config", "err", err)
		os.Exit(1)
	}
	a := &app{
		client:   edupay.NewClient(),
		store:    &store.Store{DB: dynamodb.NewFromConfig(cfg), Table: mustEnv("TABLE_NAME")},
		notifier: &notify.SNS{Client: sns.NewFromConfig(cfg), TopicARN: mustEnv("TOPIC_ARN")},
	}
	lambda.Start(a.handle)
}

func (a *app) handle(ctx context.Context) error {
	sections, err := runSearches(ctx, a.client)
	if err != nil {
		return err
	}

	var ids []string
	for _, s := range sections {
		for _, j := range s.Jobs {
			ids = append(ids, j.ID)
		}
	}
	seen, err := a.store.Seen(ctx, ids)
	if err != nil {
		return err
	}

	var fresh []edupay.Job
	for i, s := range sections {
		sections[i].Jobs = slices.DeleteFunc(s.Jobs, func(j edupay.Job) bool { return seen[j.ID] })
		fresh = append(fresh, sections[i].Jobs...)
	}
	slog.Info("searched", "found", len(ids), "new", len(fresh))
	if len(fresh) == 0 {
		return nil
	}

	// Only look up links for new jobs: each lookup is up to a few requests.
	addLinks(ctx, a.client, sections)

	// Notify before marking seen: if publishing fails, the next run retries
	// rather than silently dropping the alert.
	if err := a.notifier.Publish(ctx, sections); err != nil {
		return err
	}
	return a.store.MarkSeen(ctx, fresh, time.Now())
}

// runSearches runs each search and returns one section per search. A job
// that appears in more than one search is listed only in the first.
func runSearches(ctx context.Context, c *edupay.Client) ([]notify.Section, error) {
	listed := map[string]bool{}
	sections := make([]notify.Section, 0, len(searches))
	for _, s := range searches {
		jobs, err := c.Search(ctx, s.Facets)
		if err != nil {
			return nil, fmt.Errorf("%s search: %w", s.Heading, err)
		}
		jobs = slices.DeleteFunc(jobs, func(j edupay.Job) bool { return listed[j.ID] })
		for _, j := range jobs {
			listed[j.ID] = true
		}
		sections = append(sections, notify.Section{Heading: s.Heading, Jobs: jobs})
	}
	return sections, nil
}

// addLinks sets each job's direct posting URL. A failed lookup is logged
// and the job is sent without a link, rather than holding up the alert.
func addLinks(ctx context.Context, c *edupay.Client, sections []notify.Section) {
	for i := range sections {
		for k := range sections[i].Jobs {
			j := &sections[i].Jobs[k]
			link, err := c.PostingURL(ctx, j.ID)
			switch {
			case err != nil:
				slog.Warn("looking up posting link", "job_id", j.ID, "err", err)
			case link == "":
				slog.Warn("no posting link found", "job_id", j.ID)
			}
			j.URL = link
		}
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("missing required environment variable", "key", key)
		os.Exit(1)
	}
	return v
}
