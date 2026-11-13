# teacher-job-notify

A Go Lambda that checks the Victorian Department of Education job board
(EduPay) every day at 5pm Melbourne time. It emails any primary teaching
jobs in Casey or Cardinia that it hasn't already told you about.

```
EventBridge Scheduler (daily 17:00 Australia/Melbourne)
  └─▶ Lambda (scraper/)
        ├─▶ EduPay: two filtered searches (full time, part time)
        ├─▶ DynamoDB: skip jobs notified in the last 30 days
        └─▶ SNS ─▶ email (address from Secrets Manager)
```

## Searches

Both searches filter on Locality = Casey or Cardinia and Role Type = Primary
Teacher. One adds Full/Part-Time = Full-Time and the other Part-Time. Each
job in the email is tagged `[ONGOING]` or `[FIXED TERM]`.

The searches are defined in [`scraper/main.go`](scraper/main.go). Group and
value names must match the filter labels on the EduPay search page.

## How the scraping works

EduPay is PeopleSoft Candidate Gateway. Ticking a filter on the search page
submits the whole form back to the server (a "postback" with
`ICAction=<checkbox id>`), and the server returns the re-rendered page. This
is why search criteria never show up in the URL. The scraper does the same
thing over plain HTTP with goquery, with no headless browser. Each search
uses its own cookie jar because the server keeps search state in the session.

## Layout

```
scraper/            Go module (Lambda code)
  main.go           searches, handler, -local mode
  internal/edupay   search page client and parser
  internal/store    DynamoDB seen-jobs table (30-day TTL)
  internal/notify   email formatting, SNS publish
infra/secrets.yaml  stack 1: Secrets Manager secret (placeholder value)
infra/app.yaml      stack 2 (SAM): Lambda, schedule, table, SNS, error alarm
deploy.sh           deploys both stacks
```

## Run locally

Prints the email you'd get right now, without using AWS:

```sh
cd scraper
go test ./...
go run . -local
```

## Deploy

Needs the AWS CLI, the SAM CLI and Go.

```sh
./deploy.sh <aws-profile>
```

On the first run the script creates the secret and prompts for the email
address to send alerts to. It saves the address to the secret, then
deploys the app. The secret's value looks like this:

```json
{"notify_email": "you@example.com"}
```

When the deploy finishes, click the link in the SNS subscription
confirmation email.

The script only prompts when it has a terminal. In CI or other
non-interactive runs, set the secret in the Secrets Manager console
(ap-southeast-2) before running it.

### Changing the email address

CloudFormation reads the secret only when it creates or updates the
subscription, so updating the secret alone doesn't change where alerts go.
After changing it, delete the old subscription in the SNS console and
redeploy, or delete and redeploy the `teacher-job-notify` stack.

## Notes

- The first run emails every matching job currently listed. After that
  you only get new ones.
- Each job links straight to its posting, in the same format as the site's
  "Share" button. That link needs a posting sequence number, which the
  search results don't show. Most jobs are 1, and re-advertised jobs are
  higher. So for each new job the scraper tries 1 to 3 until the link opens
  the posting. If none work, the email shows the job ID and a link to the
  search page instead.
- Jobs are remembered for 30 days after they're notified. A job still
  listed after that would be notified again.
- If a run fails (for example, because the site's layout changed), a
  CloudWatch alarm emails the same address.
