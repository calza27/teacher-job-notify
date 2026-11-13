#!/usr/bin/env bash
# Deploys teacher-job-notify to ap-southeast-2.
#
#   ./deploy.sh <aws-profile>
#
# 1. Deploys the secrets stack (creates the secret with a placeholder value).
# 2. If the secret is empty or still holds the placeholder, prompts for the
#    alert email address and saves it to the secret.
# 3. Builds the Go Lambda and deploys the app stack.
set -euo pipefail

profile=${1:-}
if [[ -z $profile ]]; then
  echo "usage: $0 <aws-profile>" >&2
  exit 1
fi

region=ap-southeast-2
secret_name=teacher-job-notify
secrets_stack=teacher-job-notify-secrets
app_stack=teacher-job-notify

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cd "$repo_root"

aws_args=(--profile "$profile" --region "$region")

echo "==> Deploying $secrets_stack"
aws cloudformation deploy "${aws_args[@]}" \
  --stack-name "$secrets_stack" \
  --template-file infra/secrets.yaml \
  --parameter-overrides "SecretName=$secret_name" \
  --no-fail-on-empty-changeset

echo "==> Checking secret $secret_name has been set"
secret_value=$(aws secretsmanager get-secret-value "${aws_args[@]}" \
  --secret-id "$secret_name" --query SecretString --output text)
if [[ $secret_value == *REPLACE_ME* || $secret_value != *@* ]]; then
  if [[ ! -t 0 ]]; then
    echo "The secret '$secret_name' hasn't been set, and there's no terminal to prompt for it." >&2
    echo "Run this script interactively, or set the secret in the Secrets Manager console ($region)." >&2
    exit 1
  fi

  echo "The secret '$secret_name' hasn't been set yet."
  # Simple shape check. It also rules out quotes and backslashes, so the
  # value can go straight into the JSON below.
  email_pattern='^[^[:space:]"\\@]+@[^[:space:]"\\@]+\.[^[:space:]"\\@]+$'
  while true; do
    read -r -p "Email address to send job alerts to: " notify_email
    if [[ ! $notify_email =~ $email_pattern ]]; then
      echo "That doesn't look like an email address. Try again."
      continue
    fi
    read -r -p "Send alerts to '$notify_email'? [y/N] " confirm
    [[ $confirm == [yY]* ]] && break
  done

  aws secretsmanager put-secret-value "${aws_args[@]}" \
    --secret-id "$secret_name" \
    --secret-string "{\"notify_email\":\"$notify_email\"}" >/dev/null
  echo "Saved the email address to $secret_name."
  unset notify_email confirm
fi
unset secret_value

echo "==> Running tests"
(cd scraper && go vet ./... && go test ./...)

echo "==> Building"
sam build --template-file infra/app.yaml

echo "==> Deploying $app_stack"
sam deploy "${aws_args[@]}" \
  --template-file .aws-sam/build/template.yaml \
  --stack-name "$app_stack" \
  --parameter-overrides "SecretName=$secret_name" \
  --capabilities CAPABILITY_IAM \
  --resolve-s3 \
  --no-confirm-changeset \
  --no-fail-on-empty-changeset

echo
echo "Done. On first deploy, SNS emails a subscription confirmation to the"
echo "address in the secret: click the link in it to start receiving alerts."
