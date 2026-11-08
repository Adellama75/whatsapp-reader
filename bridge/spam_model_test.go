package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClassifySpam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		answer := "ok"
		if strings.Contains(req.Messages[0].Content, "tickets") {
			answer = "Spam"
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": answer}}})
	}))
	defer srv.Close()
	anthropicURL = srv.URL
	t.Setenv("ANTHROPIC_API_KEY", "test")
	for text, want := range map[string]bool{"3x tickets, DM if interested": true, "see you at the social": false} {
		if got, err := classifySpam(context.Background(), "m", text); err != nil || got != want {
			t.Errorf("classifySpam(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := classifySpam(context.Background(), "m", "x"); err == nil {
		t.Error("want an error without a key")
	}
}
