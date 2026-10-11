package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSecret = "app-secret"

type fakeForwarder struct {
	sent []string
	err  error
}

func (f *fakeForwarder) Send(_ context.Context, content string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, content)
	return nil
}

func newTestHandler(f forwarder) *webhookHandler {
	return &webhookHandler{
		verifyToken: "verify-me",
		appSecret:   []byte(testSecret),
		discord:     f,
		seen:        newDedupe(time.Hour),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func sign(body string) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func post(h http.Handler, body, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const textPayload = `{
  "object": "whatsapp_business_account",
  "entry": [{
    "id": "123",
    "changes": [{
      "field": "messages",
      "value": {
        "messaging_product": "whatsapp",
        "contacts": [{"wa_id": "15551234567", "profile": {"name": "Alice"}}],
        "messages": [{
          "from": "15551234567",
          "id": "wamid.ABC",
          "timestamp": "1700000000",
          "type": "text",
          "text": {"body": "hello @everyone"}
        }]
      }
    }]
  }]
}`

func TestVerifyHandshake(t *testing.T) {
	h := newTestHandler(&fakeForwarder{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/webhook?hub.mode=subscribe&hub.verify_token=verify-me&hub.challenge=42", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "42" {
		t.Fatalf("got %d %q, want 200 \"42\"", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/webhook?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=42", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

func TestRejectsBadSignature(t *testing.T) {
	f := &fakeForwarder{}
	h := newTestHandler(f)
	for _, sig := range []string{"", "sha256=deadbeef", sign(textPayload + " ")} {
		if rec := post(h, textPayload, sig); rec.Code != http.StatusUnauthorized {
			t.Errorf("signature %q: got %d, want 401", sig, rec.Code)
		}
	}
	if len(f.sent) != 0 {
		t.Fatalf("forwarded %d messages despite bad signature", len(f.sent))
	}
}

func TestForwardsTextAndDedupes(t *testing.T) {
	f := &fakeForwarder{}
	h := newTestHandler(f)

	for range 2 {
		if rec := post(h, textPayload, sign(textPayload)); rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200", rec.Code)
		}
	}
	want := "**Alice (+15551234567)**\nhello @everyone"
	if len(f.sent) != 1 || f.sent[0] != want {
		t.Fatalf("sent %q, want exactly [%q]", f.sent, want)
	}
}

func TestDiscordFailureAllowsRetry(t *testing.T) {
	f := &fakeForwarder{err: errors.New("boom")}
	h := newTestHandler(f)
	if rec := post(h, textPayload, sign(textPayload)); rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d, want 502", rec.Code)
	}
	f.err = nil
	if rec := post(h, textPayload, sign(textPayload)); rec.Code != http.StatusOK || len(f.sent) != 1 {
		t.Fatalf("redelivery: got %d with %d sent, want 200 with 1", rec.Code, len(f.sent))
	}
}

func TestIgnoresStatusUpdates(t *testing.T) {
	body := `{"object":"whatsapp_business_account","entry":[{"changes":[{"field":"messages","value":{"statuses":[{"id":"wamid.X","status":"delivered"}]}}]}]}`
	f := &fakeForwarder{}
	if rec := post(newTestHandler(f), body, sign(body)); rec.Code != http.StatusOK || len(f.sent) != 0 {
		t.Fatalf("got %d with %d sent, want 200 with 0", rec.Code, len(f.sent))
	}
}

func TestMessageBody(t *testing.T) {
	cases := []struct {
		json string
		want string
	}{
		{`{"type":"image","image":{"caption":"look"}}`, "📷 Image: look"},
		{`{"type":"document","document":{"filename":"a.pdf"}}`, "📄 Document (a.pdf)"},
		{`{"type":"location","location":{"latitude":1.5,"longitude":2,"name":"Home"}}`,
			"📍 Location — Home\nhttps://maps.google.com/?q=1.500000,2.000000"},
		{`{"type":"reaction","reaction":{"emoji":"👍"}}`, "Reacted 👍"},
		{`{"type":"interactive","interactive":{"button_reply":{"title":"Yes"}}}`, "Yes"},
		{`{"type":"unknown"}`, "_[unsupported message type: unknown]_"},
	}
	for _, c := range cases {
		var m message
		if err := json.Unmarshal([]byte(c.json), &m); err != nil {
			t.Fatal(err)
		}
		if got := messageBody(m); got != c.want {
			t.Errorf("%s: got %q, want %q", c.json, got, c.want)
		}
	}
}
