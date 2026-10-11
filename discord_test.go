package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDiscordSendRetriesRateLimitAndDisablesMentions(t *testing.T) {
	var calls int
	var got discordMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"retry_after":0.01}`))
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &discordClient{webhookURL: srv.URL, http: srv.Client()}
	if err := c.Send(context.Background(), strings.Repeat("é", 3000)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("got %d calls, want 2", calls)
	}
	if got.AllowedMentions.Parse == nil || len(got.AllowedMentions.Parse) != 0 {
		t.Fatalf("allowed_mentions.parse = %v, want empty list", got.AllowedMentions.Parse)
	}
	if n := utf8.RuneCountInString(got.Content); n != discordMaxContent {
		t.Fatalf("content length %d, want %d", n, discordMaxContent)
	}
}

func TestDiscordSendReturnsErrorOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := &discordClient{webhookURL: srv.URL, http: srv.Client()}
	if err := c.Send(context.Background(), "hi"); err == nil {
		t.Fatal("expected error")
	}
}
