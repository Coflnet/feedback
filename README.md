# feedback

## env vars

### DB_HOST

### DB_PORT

### DB_USER

### DB_PASSWORD

### DB_NAME

### WEBHOOK_URL
Discord webhook the feedback endpoint forwards to.

## Error reports and Discord delivery

Submit `feedbackName: "web-error"` with a JSON-encoded `feedback` object containing
`reportId` and `error.message`. A human-written `additionalInformation` comment is
optional for these reports. `FeedbackName` and the legacy typo `fedbackName` are
also accepted. Ordinary feedback keeps its existing comment requirements.

Error reports appear in Discord as a short summary with the report ID, database
ID, URL, timestamp, error message, trace ID and Next.js digest when provided.
Download **feedback.json** for the complete original report, including
`error.stack`, nested `error.cause` stacks, `errorLog` and browser metadata.
Only the message preview is shortened. Other feedback exceeding Discord's
2,000-character content limit uses the same attachment mechanism. Mentions are
disabled, and `wait=true` requests confirmation that Discord saved the message.

The full report is stored in `feedbacks.feedback` before the webhook is called.
Search `feedback.discord.delivery.attempt` / `.completed` logs by `reportId`;
failed delivery logs include the database `feedbackId` and Discord's bounded
error response body. Recover a stored report using its report reference:

```sql
SELECT id, created_at, feedback
FROM feedbacks
WHERE feedback LIKE '%<reportId>%'
ORDER BY created_at;
```

Discord failures return `502` so the client can retry. Duplicate storage detection
does not suppress another delivery attempt. Repeated submissions can therefore
produce duplicate Discord messages; use `reportId` to recognize the same report.
This endpoint has no background delivery worker: automatic delivery after a crash
or an abandoned retry would require a durable Discord outbox, as used for the
separate email flows. Stored reports remain available for manual lookup.

Deploy this service update before the frontend's comment-free error-report button.
For server errors, use `error.digest` plus timestamp/path to find the frontend's
`web.request.error` log, then follow its trace ID if present. Browser reports cannot
recover server stacks hidden by Next.js. Minified browser frames require source
maps from the matching frontend build to resolve original source locations.

## contact form (landing page)

`POST /api/contact-form` receives the landing page contact form and delivers it
to staff by email over the service's existing SMTP configuration (the same
one used for legal-action receipts). The submission is first written to a
CockroachDB outbox so delivery survives restarts; a background worker then
sends it and retries transient SMTP failures with backoff. The outbox row is
deleted as soon as the email is sent, so the submitted name/email/message are
retained only for as long as delivery is pending. The email's `Reply-To` is
set to the submitter's own address so staff can answer directly, and its
subject is `Contact form: <first words of the message>`. The endpoint returns
`500` without queuing if SMTP or the destination inbox is not configured, or
if the durable write fails. The sender name is optional; email and message are
required so staff can answer.

Contact-form messages previously went to a private Discord channel via a
dedicated webhook, with a 96-hour deletion job for each posted message. That
path has been removed: Discord webhooks could not reliably guarantee the
promised deletion in production, so delivery moved to email, whose outbox row
is deleted immediately after a successful send instead of on a delay.

The form is protected by several anti-spam layers:

1. **Honeypot** – a hidden `website` field; any value is dropped.
2. **Proof-of-work challenge** – the browser must `GET /api/contact-form/challenge`
   and solve `sha256(challenge + nonce)` with `CONTACT_POW_DIFFICULTY` leading
   hex zeros before it may submit. Signed with an HMAC so it can't be forged.
3. **Timing + replay** – challenges must be a few seconds old, expire after 20
   minutes and can be used only once.
4. **Content blacklist / scoring** – known spam domains (link shorteners,
   telegra.ph, …), crypto/gambling/SEO/job-scam phrases, link heuristics and
   foreign-language "what's your price" pings are rejected.

The honeypot and content blacklist (layers a human never trips) drop the
message silently with `200` so bots can't tell they were caught. Protocol
failures (invalid/expired/replayed challenge, bad proof-of-work, malformed
fields) return `400` so a real client retries instead of showing a false
success. The browser fetches and solves the challenge as soon as the user
starts filling the form and waits out the min-fill window locally, so a
legitimate submit is never rejected for timing.

### CONTACT_INBOX

Mailbox contact-form submissions are delivered to. Falls back to
`LEGAL_ACTION_INBOX` when unset; if set, it must be a valid address or the
contact form fails closed rather than silently falling back. Contact-form
delivery also requires `SMTP_DPA_VERIFIED` and the rest of the `SMTP_*`
settings documented under [legal contract actions](#legal-contract-actions)
below, since it reuses that same SMTP configuration.

### CONTACT_POW_DIFFICULTY
Number of leading hex zeros required in the proof-of-work (default `4`, max `8`).

### CONTACT_CHALLENGE_SECRET
HMAC secret for signing challenges. If unset, an ephemeral random secret is
generated at startup (challenges won't survive a restart). Replay state is
in-memory, so keep this service at one replica unless that state is moved to a
shared store.

### CONTACT_BLOCKLIST
Optional comma-separated extra keywords to reject, applied without a redeploy.

## legal contract actions

`POST /api/legal-action` is a separate acceptance path for withdrawal and
cancellation declarations. It reuses the signed proof-of-work challenge, but
never applies the contact-form honeypot or spam blacklist. A `201` response is
returned only after the complete declaration, server-generated reference and
server receipt time have been committed to CockroachDB together with its email
delivery state. The JSON response immediately contains the same reference and
a complete durable text receipt. A background worker sends that exact persisted
receipt to the requester and copies the configured legal inbox in the same SMTP
transaction. That inbox is also the message's `Reply-To`, so replies reach the
responsible staff mailbox directly without a duplicate internal-review email.
The outbox stores only a foreign-key reference to the legal-action record, not a
second copy of its personal data.

Every request must include a client-generated UUID v4 in `submissionId`.
If the client loses the response, it retries the same declaration and
`submissionId` with a fresh proof-of-work challenge. The unique database key
then returns the original reference, timestamp and receipt without creating
another declaration or outbox job. Reusing the ID for different content
returns `409`.

For withdrawal, name, contract identification and the receipt email channel are
required under § 356a(2)–(3) BGB. Cancellation requires the same fields so the
requester and contract are unambiguous and the confirmation can be transmitted
as required by § 312k(2)–(4) BGB. The email address must be valid. Cancellation
`terminationType` defaults to
`ordinary`; `requestedEnd` defaults to the earliest possible date and, when
supplied, is normalized to that choice or an ISO date. The
extraordinary-termination reason is also optional. Supplied structured values
are persisted and reproduced in the server and email receipts.

The endpoint fails closed with `503` before accepting a declaration when
durable storage or the retention setting is unavailable. SMTP and the legal
mailbox are delivery channels, not acceptance prerequisites: if either is
temporarily unavailable, the declaration is still accepted and its
transactional delivery job stays queued until the service restarts with valid
configuration. Port `465` uses implicit TLS; all other ports must advertise
STARTTLS. TLS 1.2 or newer is required.

### SMTP_DPA_VERIFIED

Must equal `true`. Set this attestation only after the selected mail
provider's recipient entity, DPA or other applicable role terms, processing
region and international-transfer safeguard have been verified and
documented.

### SMTP_HOST

SMTP server hostname.

### SMTP_PORT

SMTP server port.

### SMTP_USER

SMTP authentication user.

### SMTP_PASSWORD

SMTP authentication password. Leave empty to connect without SMTP authentication.

### SMTP_FROM

Receipt sender, either an email address or a mailbox such as
`Coflnet Legal <legal@example.com>`.

### LEGAL_ACTION_INBOX

Required legal mailbox for operational review and processing of every accepted
withdrawal or cancellation. It must be a valid address whose access is limited
to authorized personnel. The requester receipt includes this mailbox as both
`Cc` and `Reply-To`; the SMTP transaction is considered complete only after the
server accepts both the requester and legal recipients.

### LEGAL_ACTION_RETENTION_YEARS

Defaults to `6`, the number of full calendar years after the receipt year for
received and sent commercial letters under § 257 HGB. If set, it must equal
`6`; another value disables acceptance rather than silently changing the
documented retention period. Each accepted record receives an indexed expiry
at the start of the following year. A daily job deletes expired records only
after the requester-and-legal receipt transaction is complete and while no
legal hold is set.

### retention policy required before production

The configured period and backup-erasure schedule must remain documented in
the controller's retention register. Pending receipts and records under legal
hold are not automatically purged. The legal inbox must have an owned review
workflow and monitoring; delivery to that mailbox is the hand-off, not proof
that the requested contract action was completed.


## example

example post body

````
{
    "Feedback": "{\"key1\": \"value1\", \"key2\": \"value2\"}",
    "User": "user1",
    "Context": "project1",
    "FeedbackName": "feedbackForPurpose1"
}
````
