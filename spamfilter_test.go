package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

// real samples that got through the old, unfiltered endpoint
var spamSamples = []struct{ name, email, message string }{
	{"", "7ybzk5zn8nz14l@web-library.net", "Transaction to you.GET >> graph.org/BALANCE-36824-US-DOLLARS-04-24-2?hs=1c464c13d75ee06cb1a6697f9c3cf619& <<<  zdy5rz"},
	{"Robertjerly", "zekisuquc419@gmail.com", "Salam, qiymətinizi bilmək istədim."},
	{"IsaacHoono", "myhrtsdrm60@gmail.com", "IMPORTANT MESSAGE! WITHDRAW 1.3426 BTC BEFORE THE DAILY CYCLE ENDS https://qrlinkgenerator.com/kLrUG"},
	{"Tammie Tonga", "tonga.tammie@gmail.com", "Attract keyword-targeted visitors from specific locations with our AI-driven solution, a cost-effective alternative to paid advertising. https://cutt.ly/Xt4CHP2t"},
	{"Alejandra Barkley", "alejandra.barkley51@gmail.com", "Blind upgrades are the quickest way to waste budget on hardware. https://fpsbench.com/ https://pristinetraffic.com/rate-my-pc simply fill the form at brnd .li/delist webpage with your domain address"},
	{"Robertjerly", "zekisuquc419@gmail.com", "Hai, saya ingin tahu harga Anda."},
	{"Selina Ebert", "ebert.selina@gmail.com", "Our AI-optimized traffic solution sends engaged, keyword-specific visitors to your site. https://cutt.ly/Vt4CHYR6"},
	{"IsaacHoono", "diego6215@gmail.com", "The $27,000,000 Jackpot Is a Crown for Cash https://tau.lu/09eb069b3 CLAIM YOUR $25,000 BONUS"},
	{"Yasuhiro Yamada", "rohtopharmacy5@gmail.com", "We need you to serve as our Spokesperson/Financial Coordinator for our company. It's a part-time job with a minimum salary of $5k"},
	{"", "be931gpebogyeh@web-library.net", "Balance +1,824868 btc. Next -> telegra.ph/COMPENSATION-05-12-9?hs=1c464c13d75ee06cb1a6697f9c3cf619& 5v2ppm"},
}

func TestSpamSamplesBlocked(t *testing.T) {
	for i, s := range spamSamples {
		score, why := spamScore(s.name, s.email, s.message)
		if score < spamRejectThreshold {
			t.Errorf("sample %d NOT blocked (score %d, %q): %q", i, score, why, s.message)
		}
	}
}

func TestLegitMessagesPass(t *testing.T) {
	legit := []struct{ name, email, message string }{
		{"Jane Doe", "jane@example.com", "Hi, we run a small Minecraft server and would love to talk about your filter system. When are you available for a call?"},
		{"Max Mustermann", "max@firma.de", "Hallo, wir interessieren uns fuer eine Zusammenarbeit im Bereich Datenverarbeitung. Koennen wir einen Termin vereinbaren?"},
		{"Sam Rivera", "sam.rivera@acme.io", "Loved your talk at the meetup. Could you send over more details about the SkyBlock project scope and pricing?"},
	}
	for i, s := range legit {
		score, why := spamScore(s.name, s.email, s.message)
		if score >= spamRejectThreshold {
			t.Errorf("legit message %d wrongly blocked (score %d, %q): %q", i, score, why, s.message)
		}
	}
}

func TestPoWRoundTrip(t *testing.T) {
	h := &ContactHandler{difficulty: 3}
	challenge := "deadbeefcafebabe"
	// brute force a nonce the same way the browser does
	prefix := strings.Repeat("0", h.difficulty)
	var nonce string
	for i := 0; i < 5_000_000; i++ {
		n := strconv.Itoa(i)
		sum := sha256.Sum256([]byte(challenge + n))
		if strings.HasPrefix(hex.EncodeToString(sum[:]), prefix) {
			nonce = n
			break
		}
	}
	if nonce == "" {
		t.Fatal("could not solve PoW")
	}
	if !verifyPoW(challenge, nonce, h.difficulty) {
		t.Fatalf("verifyPoW rejected a valid nonce %q", nonce)
	}
	if verifyPoW(challenge, nonce+"x", h.difficulty) {
		t.Errorf("verifyPoW accepted a tampered nonce")
	}
}

func TestSignatureBinding(t *testing.T) {
	h := &ContactHandler{secret: []byte("test-secret")}
	sig := h.sign("abc", 1000, 4)
	if h.sign("abc", 1000, 4) != sig {
		t.Error("signature not deterministic")
	}
	if h.sign("abc", 1001, 4) == sig {
		t.Error("signature did not change with timestamp")
	}
	if h.sign("abd", 1000, 4) == sig {
		t.Error("signature did not change with challenge")
	}
}
