// Package notify formats new-job alerts and publishes them to SNS.
package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/calza27/teacher-job-notify/scraper/internal/edupay"
)

// Section is a group of jobs shown under one heading, e.g. "Full time".
type Section struct {
	Heading string
	Jobs    []edupay.Job
}

// SNS publishes alerts to a topic (an email subscription).
type SNS struct {
	Client   *sns.Client
	TopicARN string
}

// Publish sends one message listing every job in the sections.
func (n *SNS) Publish(ctx context.Context, sections []Section) error {
	_, err := n.Client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(n.TopicARN),
		Subject:  aws.String(Subject(sections)),
		Message:  aws.String(Body(sections)),
	})
	if err != nil {
		return fmt.Errorf("publishing to SNS: %w", err)
	}
	return nil
}

// Subject summarises the counts, e.g. "3 new jobs: 2 full time, 1 part time".
// SNS email subjects must be ASCII and at most 100 characters.
func Subject(sections []Section) string {
	total := 0
	var parts []string
	for _, s := range sections {
		if len(s.Jobs) > 0 {
			total += len(s.Jobs)
			parts = append(parts, fmt.Sprintf("%d %s", len(s.Jobs), strings.ToLower(s.Heading)))
		}
	}
	noun := "jobs"
	if total == 1 {
		noun = "job"
	}
	return fmt.Sprintf("%d new %s: %s", total, noun, strings.Join(parts, ", "))
}

// Body returns a plain-text email. SNS email can't render HTML, so the
// employment type is highlighted with an upper-case tag. Mail clients turn
// the bare URLs into links.
func Body(sections []Section) string {
	var b strings.Builder
	missingLink := false
	for _, s := range sections {
		if len(s.Jobs) == 0 {
			continue
		}
		heading := fmt.Sprintf("%s (%d)", strings.ToUpper(s.Heading), len(s.Jobs))
		fmt.Fprintf(&b, "%s\n%s\n\n", heading, strings.Repeat("=", len(heading)))

		for _, j := range s.Jobs {
			fmt.Fprintf(&b, "[%s] %s\n", strings.ToUpper(j.Type), j.Title)
			fmt.Fprintf(&b, "    %s\n", j.School)
			details := []string{j.Hours + " hrs/week"}
			if !j.ClosingDate.IsZero() {
				details = append(details, "closes "+j.ClosingDate.Format("Mon 2 Jan 2006"))
			}
			details = append(details, "Job ID "+j.ID)
			fmt.Fprintf(&b, "    %s\n", strings.Join(details, " | "))
			if j.URL != "" {
				fmt.Fprintf(&b, "    %s\n\n", j.URL)
			} else {
				fmt.Fprintf(&b, "    (no direct link: search for the job ID, link below)\n\n")
				missingLink = true
			}
		}
	}
	if missingLink {
		fmt.Fprintf(&b, "Job search:\n%s\n", edupay.SearchURL)
	}
	return b.String()
}
