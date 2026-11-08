package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// The spam classifier: with [bridge] spam_model set (e.g. "claude-haiku-5-5") and ANTHROPIC_API_KEY
// in the bridge's environment, a message in a covered group that passes spamRule is shown to that
// Claude model, which answers "spam" or "ok". Only the message's text is sent: no sender, no group.

const spamPrompt = `You screen messages posted in UK university society and course WhatsApp groups.
Answer "spam" if the message is spam: reselling concert/event tickets privately, crypto/forex/get-rich
schemes, device giveaways, fake jobs or "easy money", unsolicited promotion asking people to DM or
click a link unrelated to the group. Answer "ok" for anything else, including a society's own
events, official ticket links, questions, chat, jokes and announcements. Answer with one word.`

var anthropicURL = "https://api.anthropic.com/v1/messages"

// classifySpam asks the model about one message's text: true for spam.
func classifySpam(ctx context.Context, model, text string) (bool, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return false, fmt.Errorf("no ANTHROPIC_API_KEY")
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "max_tokens": 5, "system": spamPrompt,
		"messages": []map[string]string{{"role": "user", "content": text}},
	})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", anthropicURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	if ws := os.Getenv("ANTHROPIC_WORKSPACE_ID"); ws != "" { // user-scoped keys name a workspace
		req.Header.Set("anthropic-workspace-id", ws)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, fmt.Errorf("status %d: %w", resp.StatusCode, err)
	}
	if out.Error != nil {
		return false, fmt.Errorf("status %d: %s", resp.StatusCode, out.Error.Message)
	}
	var answer string
	for _, c := range out.Content {
		answer += c.Text
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "spam"), nil
}
