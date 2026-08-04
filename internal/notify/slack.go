// Package notify posts alerts to a Slack incoming webhook.
//
// A webhook rather than a bot token, deliberately: a bot posting with a user
// token arrives as if the user sent it, and Slack never notifies you about
// your own messages. That was found the hard way — alerts delivered
// successfully and silently ignored. An incoming webhook posts as an app,
// which notifies normally.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Alert is one actionable condition.
type Alert struct {
	Title string
	Body  string
}

// Slack posts to an incoming webhook.
type Slack struct {
	Username  string
	IconEmoji string
	HTTP      *http.Client
}

// New returns a Slack poster.
func New(username, icon string) *Slack {
	return &Slack{
		Username:  username,
		IconEmoji: icon,
		HTTP:      &http.Client{Timeout: 15 * time.Second},
	}
}

type payload struct {
	Text      string `json:"text"`
	Username  string `json:"username,omitempty"`
	IconEmoji string `json:"icon_emoji,omitempty"`
}

// Post sends alerts as a single message. Empty input sends nothing — silence
// is the normal state, and an "all clear" every morning trains you to ignore
// the channel.
//
// webhookURL is passed per-call rather than stored so that a rotated secret is
// picked up without a restart.
func (s *Slack) Post(ctx context.Context, webhookURL string, alerts []Alert) error {
	if len(alerts) == 0 {
		return nil
	}
	if webhookURL == "" {
		return fmt.Errorf("notify: no webhook configured")
	}

	var b strings.Builder
	for i, a := range alerts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(a.Title)
		if a.Body != "" {
			b.WriteString("\n")
			b.WriteString(a.Body)
		}
	}

	body, err := json.Marshal(payload{
		Text:      b.String(),
		Username:  s.Username,
		IconEmoji: s.IconEmoji,
	})
	if err != nil {
		return fmt.Errorf("notify: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("notify: post to webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// Deliberately does not include the URL — it is a credential.
		return fmt.Errorf("notify: webhook returned status %d", resp.StatusCode)
	}
	return nil
}
