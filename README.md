# feedback

## env vars

### DB_HOST

### DB_PORT

### DB_USER

### DB_PASSWORD

### DB_NAME

### WEBHOOK_URL
Discord webhook the feedback endpoint forwards to.

## contact form (landing page)

`POST /api/contact-form` receives the landing page contact form and forwards it
to Discord. It is protected by several anti-spam layers:

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

### CONTACT_WEBHOOK_URL
Discord webhook for contact form messages (falls back to `WEBHOOK_URL`).

### CONTACT_POW_DIFFICULTY
Number of leading hex zeros required in the proof-of-work (default `4`, max `8`).

### CONTACT_CHALLENGE_SECRET
HMAC secret for signing challenges. If unset, an ephemeral random secret is
generated at startup (challenges won't survive a restart).

### CONTACT_BLOCKLIST
Optional comma-separated extra keywords to reject, applied without a redeploy.


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
