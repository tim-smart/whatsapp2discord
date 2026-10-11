package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"
)

const (
	discordMaxContent = 2000
	discordMaxRetries = 3
	discordMaxBackoff = 5 * time.Second
)

type discordClient struct {
	webhookURL string
	http       *http.Client
}

type discordMessage struct {
	Content         string                 `json:"content"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

// Send posts content to the Discord webhook, retrying on rate limits.
// Mentions are disabled so WhatsApp senders cannot ping @everyone or roles.
func (c *discordClient) Send(ctx context.Context, content string) error {
	body, err := json.Marshal(discordMessage{
		Content:         truncate(content, discordMaxContent),
		AllowedMentions: discordAllowedMentions{Parse: []string{}},
	})
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= discordMaxRetries {
			return fmt.Errorf("discord webhook returned %s: %s", resp.Status, respBody)
		}

		var rl struct {
			RetryAfter float64 `json:"retry_after"`
		}
		json.Unmarshal(respBody, &rl)
		wait := min(time.Duration(rl.RetryAfter*float64(time.Second)), discordMaxBackoff)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes-1]) + "…"
}
