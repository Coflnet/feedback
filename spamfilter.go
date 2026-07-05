package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// spamRejectThreshold: a message whose accumulated spam score reaches this is
// dropped. Any single "instant" signal (blocked domain, spam phrase) already
// scores at or above it, while softer heuristics stack up toward it.
const spamRejectThreshold = 100

// blockedDomains are hosts that essentially only appear in the spam we get:
// link shorteners and anonymous publishing platforms used to hide payloads.
// Presence of any of these is an instant block.
var blockedDomains = []string{
	"graph.org", "telegra.ph", "telegraph.",
	"cutt.ly", "tau.lu", "brnd.li", "bit.ly", "tinyurl.com",
	"qrlinkgenerator.com", "t.me", "is.gd", "rebrand.ly",
	"shorturl", "rb.gy", "lnkd.in", "web-library.net",
}

// spamPhrases are strong content signals lifted from real submissions. Each is
// an instant block. Kept lowercase; matching is case-insensitive.
var spamPhrases = []string{
	// crypto / withdrawal / lottery scams
	"btc", "bitcoin", "withdraw", "jackpot", "promo code", "crypto",
	"you mined", "compensation", "transaction to you", "daily cycle",
	"claim your", "bonus and chase", "cheat code", "balance +",
	// seo / traffic spam
	"targeted traffic", "keyword-targeted", "ai-driven traffic",
	"ai-optimized traffic", "ai targeted traffic", "paid ads",
	"drive results", "high-converting", "rate my pc", "fpsbench",
	"upgrade delivers", "cost-effective alternative",
	// job / recruitment scams
	"spokesperson", "financial coordinator", "part-time job",
	"minimum salary", "conflict of interest",
	// mass-translated "I want to know your price" fishing pings. These are
	// non-English/German terms a legitimate visitor here would not type.
	"qiymət", "qiymet", "bilmək istədim", "ingin tahu harga",
	"harga anda", "fiyat", "prezzo", "precio", "preço", "цена", "цену",
	// opt-out / delist footers used by bulk mailers
	"opt-out", "opt out", "delist", "future emails", "unsubscribe",
	"receive future", "subsequent communications",
}

// urlRegex finds http(s) links and bare domains in free text.
var urlRegex = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+|\b[a-z0-9-]+\.(?:com|net|org|ph|ly|lu|li|io|xyz|top|ru|info|biz|gd|me)\b`)

// nonLatinRegex matches scripts that never appear in our (English/German)
// contact traffic but are common in the localized "what is your price" spam.
var nonLatinRegex = regexp.MustCompile(`[\p{Han}\p{Cyrillic}\p{Arabic}\p{Hangul}\p{Hiragana}\p{Katakana}]`)

// extraBlocklist lets operators add keywords at runtime via the
// CONTACT_BLOCKLIST env var (comma separated) without a redeploy.
func extraBlocklist() []string {
	v := strings.TrimSpace(os.Getenv("CONTACT_BLOCKLIST"))
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// spamScore rates a submission. It returns the accumulated score and a short
// human-readable reason for logging. Callers reject at spamRejectThreshold.
func spamScore(name, email, message string) (int, string) {
	haystack := strings.ToLower(name + "\n" + email + "\n" + message)
	var score int
	var reasons []string

	add := func(points int, reason string) {
		score += points
		reasons = append(reasons, reason)
	}

	// Instant blocks: known bad domains.
	for _, d := range blockedDomains {
		if strings.Contains(haystack, d) {
			add(spamRejectThreshold, "blocked-domain:"+d)
			return score, strings.Join(reasons, ",")
		}
	}

	// Instant blocks: spam phrases (default + operator-supplied).
	phrases := append(spamPhrases, extraBlocklist()...)
	for _, p := range phrases {
		if strings.Contains(haystack, p) {
			add(spamRejectThreshold, "phrase:"+p)
			return score, strings.Join(reasons, ",")
		}
	}

	// Soft signals that stack up.
	urls := urlRegex.FindAllString(message, -1)
	switch {
	case len(urls) >= 2:
		add(80, fmt.Sprintf("links:%d", len(urls)))
	case len(urls) == 1:
		add(50, "links:1")
	}

	if nonLatinRegex.MatchString(message) {
		add(60, "non-latin-script")
	}

	// A "name" that is a single run-together token with mixed inner casing
	// (Robertjerly, IsaacHoono) is a common bot signature.
	if runTogetherName(name) {
		add(40, "run-together-name")
	}

	// Very short messages that still contain a link are almost always spam.
	if len(urls) > 0 && len(strings.Fields(message)) < 6 {
		add(50, "short-message-with-link")
	}

	return score, strings.Join(reasons, ",")
}

// runTogetherName reports whether a name looks like "Robertjerly" or
// "IsaacHoono": a single whitespace-free token containing a lowercase letter
// immediately followed by an uppercase letter, or an unusually long single word.
func runTogetherName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t") {
		return false
	}
	runes := []rune(name)
	for i := 1; i < len(runes); i++ {
		if runes[i-1] >= 'a' && runes[i-1] <= 'z' && runes[i] >= 'A' && runes[i] <= 'Z' {
			return true
		}
	}
	return len([]rune(name)) > 20
}
