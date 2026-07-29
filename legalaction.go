package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
)

type LegalActionStore interface {
	// AcceptLegalAction atomically persists a new declaration and its requester
	// confirmation/internal-review delivery states, or returns the record
	// already accepted for the submission ID.
	AcceptLegalAction(*LegalAction) (*LegalAction, bool, error)
}

type LegalAction struct {
	Reference          string     `gorm:"primaryKey;size:35" json:"reference"`
	SubmissionID       string     `gorm:"size:36;uniqueIndex" json:"submissionId"`
	ReceivedAt         time.Time  `gorm:"not null;index" json:"receivedAt"`
	Action             string     `gorm:"not null;size:16" json:"action"`
	Language           string     `gorm:"not null;size:2" json:"language"`
	Name               string     `gorm:"not null;size:200" json:"name"`
	Email              string     `gorm:"not null;size:254" json:"email"`
	ContractIdentifier string     `gorm:"not null;size:1000" json:"contractIdentifier"`
	Scope              string     `gorm:"type:text" json:"scope,omitempty"`
	TerminationType    string     `gorm:"size:32" json:"terminationType,omitempty"`
	Reason             string     `gorm:"type:text" json:"reason,omitempty"`
	RequestedEnd       string     `gorm:"size:64" json:"requestedEnd,omitempty"`
	Declaration        string     `gorm:"type:text;not null" json:"declaration"`
	ExpiresAt          *time.Time `gorm:"index" json:"expiresAt,omitempty"`
	LegalHold          bool       `gorm:"not null;default:false;index" json:"-"`
}

type legalActionResponse struct {
	Reference  string    `json:"reference"`
	ReceivedAt time.Time `json:"receivedAt"`
	Receipt    string    `json:"receipt"`
}

var submissionIDRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (h *ContactHandler) postLegalAction(c *fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	if h.legalStore == nil || !h.legalActionsConfigured {
		return fiber.NewError(http.StatusServiceUnavailable, "legal action storage unavailable")
	}
	if layer, reason := h.validateChallenge(c); layer != "" {
		return h.rejectBad(c, layer, reason)
	}

	submissionID, err := requiredLegalField(c, "submissionId", 36)
	submissionID = strings.ToLower(submissionID)
	if err != nil || !submissionIDRegex.MatchString(submissionID) {
		return legalActionBadRequest(c, "invalid submissionId")
	}
	action, err := requiredLegalField(c, "action", 16)
	if err != nil || (action != "withdrawal" && action != "cancellation") {
		return legalActionBadRequest(c, "invalid action")
	}
	language, err := requiredLegalField(c, "language", 2)
	if err != nil || (language != "en" && language != "de") {
		return legalActionBadRequest(c, "invalid language")
	}
	name, err := optionalLegalField(c, "name", 200)
	if err != nil {
		return legalActionBadRequest(c, err.Error())
	}
	email, err := optionalLegalField(c, "email", 254)
	if err != nil || (email != "" && !looksLikeEmail(email)) {
		return legalActionBadRequest(c, "invalid email")
	}
	contract, err := optionalLegalField(c, "contract", 1000)
	if err != nil {
		return legalActionBadRequest(c, err.Error())
	}
	if name == "" || email == "" || contract == "" {
		return legalActionBadRequest(c, "name, email and contract are required")
	}
	scope := ""
	reason := ""
	terminationType := ""
	requestedEnd := ""
	if action == "withdrawal" {
		scope, err = optionalLegalField(c, "scope", 2000)
		if err != nil {
			return legalActionBadRequest(c, err.Error())
		}
	} else {
		terminationType, err = optionalLegalField(c, "terminationType", 32)
		if err != nil {
			return legalActionBadRequest(c, err.Error())
		}
		if terminationType == "" {
			terminationType = "ordinary"
		}
		if terminationType != "ordinary" && terminationType != "extraordinary" {
			return legalActionBadRequest(c, "invalid terminationType")
		}
		requestedEnd, err = optionalLegalField(c, "requestedEnd", 64)
		if err != nil {
			return legalActionBadRequest(c, err.Error())
		}
		if requestedEnd == "" {
			requestedEnd = "earliest"
		}
		requestedEnd, err = normalizeRequestedEnd(requestedEnd)
		if err != nil {
			return legalActionBadRequest(c, err.Error())
		}
		if terminationType == "extraordinary" {
			reason, err = optionalLegalField(c, "reason", 5000)
			if err != nil {
				return legalActionBadRequest(c, err.Error())
			}
		}
	}

	reference, err := newLegalActionReference()
	if err != nil {
		return fiber.NewError(http.StatusInternalServerError, "could not create reference")
	}
	receivedAt := time.Now().UTC()
	expiresAt := legalActionExpiry(receivedAt, h.legalRetentionYears)
	record := &LegalAction{
		Reference:          reference,
		SubmissionID:       submissionID,
		ReceivedAt:         receivedAt,
		Action:             action,
		Language:           language,
		Name:               name,
		Email:              email,
		ContractIdentifier: contract,
		Scope:              scope,
		TerminationType:    terminationType,
		Reason:             reason,
		RequestedEnd:       requestedEnd,
		ExpiresAt:          &expiresAt,
	}
	record.Declaration = record.canonicalDeclaration()
	accepted, created, err := h.legalStore.AcceptLegalAction(record)
	if err != nil || accepted == nil {
		return fiber.NewError(http.StatusInternalServerError, "could not persist legal action and receipt delivery")
	}
	if !created && !sameLegalActionSubmission(accepted, record) {
		return c.Status(http.StatusConflict).JSON(fiber.Map{
			"error": "submissionId was already used for a different declaration",
		})
	}
	record = accepted

	return c.Status(http.StatusCreated).JSON(legalActionResponse{
		Reference:  record.Reference,
		ReceivedAt: record.ReceivedAt,
		Receipt:    record.receipt(),
	})
}

func sameLegalActionSubmission(left, right *LegalAction) bool {
	return left.SubmissionID == right.SubmissionID &&
		left.Action == right.Action &&
		left.Language == right.Language &&
		left.Name == right.Name &&
		left.Email == right.Email &&
		left.ContractIdentifier == right.ContractIdentifier &&
		left.Scope == right.Scope &&
		left.TerminationType == right.TerminationType &&
		left.Reason == right.Reason &&
		left.RequestedEnd == right.RequestedEnd &&
		left.Declaration == right.Declaration
}

func legalActionExpiry(receivedAt time.Time, years int) time.Time {
	// German commercial-letter retention begins at the end of the receipt year.
	return time.Date(receivedAt.Year()+years+1, time.January, 1, 0, 0, 0, 0, time.UTC)
}

func requiredLegalField(c *fiber.Ctx, name string, limit int) (string, error) {
	value := strings.TrimSpace(c.FormValue(name))
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return "", fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

func optionalLegalField(c *fiber.Ctx, name string, limit int) (string, error) {
	value := strings.TrimSpace(c.FormValue(name))
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return "", fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

func normalizeRequestedEnd(value string) (string, error) {
	switch value {
	case "earliest", "earliest possible date", "At the earliest possible date", "Zum nächstmöglichen Zeitpunkt":
		return "earliest", nil
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return "", fmt.Errorf("invalid requestedEnd")
	}
	return value, nil
}

func legalActionBadRequest(c *fiber.Ctx, message string) error {
	return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": message})
}

func newLegalActionReference() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "LA-" + hex.EncodeToString(value), nil
}

func (a *LegalAction) requestedEndText() string {
	if a.RequestedEnd == "earliest" {
		if a.Language == "de" {
			return "zum nächstmöglichen Zeitpunkt"
		}
		return "at the earliest possible date"
	}
	if a.Language == "de" {
		if parsed, err := time.Parse("2006-01-02", a.RequestedEnd); err == nil {
			return "zum " + parsed.Format("02.01.2006")
		}
		return "zum " + a.RequestedEnd
	}
	return "on " + a.RequestedEnd
}

func (a *LegalAction) canonicalDeclaration() string {
	if a.Action == "withdrawal" {
		if a.Language == "de" {
			if a.Scope != "" {
				return "Hiermit widerrufe ich den bezeichneten Vertrag, beschränkt auf: " + a.Scope + "."
			}
			return "Hiermit widerrufe ich den bezeichneten Vertrag."
		}
		if a.Scope != "" {
			return "I hereby withdraw from the identified contract, limited to: " + a.Scope + "."
		}
		return "I hereby withdraw from the identified contract."
	}
	if a.Language == "de" {
		declaration := "Hiermit kündige ich den bezeichneten Vertrag " + map[string]string{
			"ordinary":      "ordentlich ",
			"extraordinary": "außerordentlich ",
		}[a.TerminationType] + a.requestedEndText() + "."
		if a.TerminationType == "extraordinary" && a.Reason != "" {
			declaration += " Grund: " + a.Reason
		}
		return declaration
	}
	declaration := "I hereby give " + a.TerminationType +
		" notice to terminate the identified contract " + a.requestedEndText() + "."
	if a.TerminationType == "extraordinary" && a.Reason != "" {
		declaration += " Reason: " + a.Reason
	}
	return declaration
}

func (a *LegalAction) receipt() string {
	receivedAt := a.ReceivedAt.Format(time.RFC3339Nano)
	if a.Language == "de" {
		action := "Widerruf"
		button := "Widerruf bestätigen"
		if a.Action == "cancellation" {
			action = "Kündigung"
			button = "jetzt kündigen"
		}
		lines := []string{
			"Coflnet GmbH — elektronische Eingangsbestätigung",
			"Referenz: " + a.Reference,
			"Abgabe über die Bestätigungsschaltfläche „" + button + "“: " + receivedAt,
			"Zugang bei Coflnet: " + receivedAt,
			"Art: " + action,
		}
		if a.Name != "" {
			lines = append(lines, "Name: "+a.Name)
		}
		if a.Email != "" {
			lines = append(lines, "Elektronische Kontaktadresse: "+a.Email)
		}
		if a.ContractIdentifier != "" {
			lines = append(lines, "Vertrag: "+a.ContractIdentifier)
		}
		if a.Scope != "" {
			lines = append(lines, "Betroffener Vertragsteil: "+a.Scope)
		}
		if a.Action == "cancellation" {
			terminationType := "ordentlich"
			if a.TerminationType == "extraordinary" {
				terminationType = "außerordentlich"
			}
			lines = append(lines,
				"Kündigungsart: "+terminationType,
				"Gewünschter Beendigungszeitpunkt: "+a.requestedEndText(),
			)
			if a.TerminationType == "extraordinary" && a.Reason != "" {
				lines = append(lines, "Grund der außerordentlichen Kündigung: "+a.Reason)
			}
		}
		return strings.Join(append(lines, "Erklärung: "+a.Declaration), "\n")
	}
	action := "Withdrawal"
	button := "Confirm withdrawal"
	if a.Action == "cancellation" {
		action = "Cancellation"
		button = "Cancel now"
	}
	lines := []string{
		"Coflnet GmbH — electronic receipt",
		"Reference: " + a.Reference,
		"Submitted using the confirmation button “" + button + "”: " + receivedAt,
		"Received by Coflnet: " + receivedAt,
		"Action: " + action,
	}
	if a.Name != "" {
		lines = append(lines, "Name: "+a.Name)
	}
	if a.Email != "" {
		lines = append(lines, "Electronic contact address: "+a.Email)
	}
	if a.ContractIdentifier != "" {
		lines = append(lines, "Contract: "+a.ContractIdentifier)
	}
	if a.Scope != "" {
		lines = append(lines, "Part of the contract concerned: "+a.Scope)
	}
	if a.Action == "cancellation" {
		terminationType := "Ordinary"
		if a.TerminationType == "extraordinary" {
			terminationType = "Extraordinary"
		}
		lines = append(lines,
			"Termination type: "+terminationType,
			"Requested end: "+a.requestedEndText(),
		)
		if a.TerminationType == "extraordinary" && a.Reason != "" {
			lines = append(lines, "Reason for extraordinary termination: "+a.Reason)
		}
	}
	return strings.Join(append(lines, "Declaration: "+a.Declaration), "\n")
}
