package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Message is the content of one notification. It deliberately carries no log
// lines, params or variables (ADR-0021).
type Message struct {
	Kind      string
	RunID     string
	Workspace string
	Template  string
	Status    string
	Trigger   string
	Check     bool
	// Host counts of the run.
	HostsOK, HostsChanged, HostsFailed, HostsUnreachable int
	Duration                                             time.Duration
	FinishedAt                                           time.Time
	// URL links to the run in the UI; empty when ACP_PUBLIC_URL is unset.
	URL string
	// Test marks a sample message sent from the channel test action.
	Test bool
	// TxnID makes retried Matrix sends idempotent (the delivery id).
	TxnID string
}

// RunURL builds the UI link of a run from the public base URL.
func RunURL(publicURL, runID string) string {
	if publicURL == "" {
		return ""
	}
	return strings.TrimRight(publicURL, "/") + "/runs/" + runID
}

var kindTitle = map[string]string{
	KindFailed:    "Run failed",
	KindRecovered: "Run recovered",
	KindDrift:     "Drift detected",
	KindFinished:  "Run finished",
}

// Title is the one-line subject.
func (m Message) Title() string {
	t := kindTitle[m.Kind]
	if t == "" {
		t = "Run " + m.Status
	}
	if m.Test {
		return "[ACP test] " + t + ": " + m.Template
	}
	return "[ACP] " + t + ": " + m.Template
}

// Text is the plain-text body.
func (m Message) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", m.Title())
	fmt.Fprintf(&b, "Workspace: %s\n", m.Workspace)
	fmt.Fprintf(&b, "Template: %s\n", m.Template)
	fmt.Fprintf(&b, "Status: %s", m.Status)
	if m.Check {
		b.WriteString(" (check mode)")
	}
	fmt.Fprintf(&b, "\nTrigger: %s\n", m.Trigger)
	fmt.Fprintf(&b, "Hosts: ok=%d changed=%d failed=%d unreachable=%d\n", m.HostsOK, m.HostsChanged, m.HostsFailed, m.HostsUnreachable)
	if m.Duration > 0 {
		fmt.Fprintf(&b, "Duration: %s\n", m.Duration.Round(time.Second))
	}
	if m.URL != "" {
		fmt.Fprintf(&b, "Link: %s\n", m.URL)
	}
	return b.String()
}

// webhookPayload is the versioned webhook body.
type webhookPayload struct {
	Version   int        `json:"version"`
	Event     string     `json:"event"`
	Test      bool       `json:"test,omitempty"`
	Workspace string     `json:"workspace"`
	Run       webhookRun `json:"run"`
}

type webhookRun struct {
	ID               string  `json:"id"`
	Template         string  `json:"template"`
	Status           string  `json:"status"`
	Trigger          string  `json:"trigger"`
	Check            bool    `json:"check"`
	HostsOK          int     `json:"hosts_ok"`
	HostsChanged     int     `json:"hosts_changed"`
	HostsFailed      int     `json:"hosts_failed"`
	HostsUnreachable int     `json:"hosts_unreachable"`
	DurationSeconds  float64 `json:"duration_seconds"`
	FinishedAt       string  `json:"finished_at,omitempty"`
	URL              string  `json:"url,omitempty"`
}

// WebhookBody renders the version 1 JSON body.
func (m Message) WebhookBody() ([]byte, error) {
	p := webhookPayload{Version: 1, Event: m.Kind, Test: m.Test, Workspace: m.Workspace, Run: webhookRun{
		ID: m.RunID, Template: m.Template, Status: m.Status, Trigger: m.Trigger, Check: m.Check,
		HostsOK: m.HostsOK, HostsChanged: m.HostsChanged, HostsFailed: m.HostsFailed, HostsUnreachable: m.HostsUnreachable,
		DurationSeconds: m.Duration.Seconds(), URL: m.URL,
	}}
	if !m.FinishedAt.IsZero() {
		p.Run.FinishedAt = m.FinishedAt.UTC().Format(time.RFC3339)
	}
	return json.Marshal(p)
}

// SampleMessage is what the channel test action sends.
func SampleMessage(workspace, publicURL string) Message {
	return Message{Kind: KindFinished, RunID: "00000000-0000-0000-0000-000000000000", Workspace: workspace,
		Template: "sample-template", Status: "succeeded", Trigger: "manual",
		HostsOK: 3, Duration: 42 * time.Second, FinishedAt: time.Now(), URL: publicURL, Test: true}
}
