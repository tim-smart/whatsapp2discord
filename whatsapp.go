package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxBodyBytes = 1 << 20

// webhookPayload is the subset of the WhatsApp Cloud API webhook payload we use.
// https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks/components
type webhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string      `json:"field"`
			Value changeValue `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type changeValue struct {
	Contacts []struct {
		WaID    string `json:"wa_id"`
		Profile struct {
			Name string `json:"name"`
		} `json:"profile"`
	} `json:"contacts"`
	Messages []message `json:"messages"`
}

type message struct {
	From      string `json:"from"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`

	Text *struct {
		Body string `json:"body"`
	} `json:"text"`
	Image    *media `json:"image"`
	Video    *media `json:"video"`
	Audio    *media `json:"audio"`
	Document *media `json:"document"`
	Sticker  *media `json:"sticker"`
	Location *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Name      string  `json:"name"`
		Address   string  `json:"address"`
	} `json:"location"`
	Reaction *struct {
		MessageID string `json:"message_id"`
		Emoji     string `json:"emoji"`
	} `json:"reaction"`
	Button *struct {
		Text string `json:"text"`
	} `json:"button"`
	Interactive *struct {
		ButtonReply *struct {
			Title string `json:"title"`
		} `json:"button_reply"`
		ListReply *struct {
			Title string `json:"title"`
		} `json:"list_reply"`
	} `json:"interactive"`
	Contacts []struct {
		Name struct {
			FormattedName string `json:"formatted_name"`
		} `json:"name"`
	} `json:"contacts"`
}

type media struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	Caption  string `json:"caption"`
	Filename string `json:"filename"`
}

type forwarder interface {
	Send(ctx context.Context, content string) error
}

type webhookHandler struct {
	verifyToken string
	appSecret   []byte
	discord     forwarder
	seen        *dedupe
	logger      *slog.Logger
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.verify(w, r)
	case http.MethodPost:
		h.receive(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// verify answers Meta's webhook verification handshake.
func (h *webhookHandler) verify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	token := q.Get("hub.verify_token")
	if q.Get("hub.mode") != "subscribe" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(h.verifyToken)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, q.Get("hub.challenge"))
}

func (h *webhookHandler) receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validSignature(h.appSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		h.logger.Warn("rejected webhook with invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}
			names := make(map[string]string, len(change.Value.Contacts))
			for _, c := range change.Value.Contacts {
				names[c.WaID] = c.Profile.Name
			}
			for _, m := range change.Value.Messages {
				if h.seen.has(m.ID) {
					continue
				}
				content := formatMessage(m, names[m.From])
				if err := h.discord.Send(r.Context(), content); err != nil {
					// A non-2xx response makes Meta redeliver the webhook later.
					// Already-forwarded messages are skipped via the dedupe set.
					h.logger.Error("forwarding to discord failed", "message_id", m.ID, "err", err)
					http.Error(w, "upstream error", http.StatusBadGateway)
					return
				}
				h.seen.add(m.ID)
				h.logger.Info("forwarded message", "message_id", m.ID, "type", m.Type)
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

func validSignature(secret, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

func formatMessage(m message, name string) string {
	sender := "+" + m.From
	if name != "" {
		sender = fmt.Sprintf("%s (+%s)", name, m.From)
	}
	return fmt.Sprintf("**%s**\n%s", sender, messageBody(m))
}

func messageBody(m message) string {
	withCaption := func(label string, md *media) string {
		if md != nil && md.Caption != "" {
			return label + ": " + md.Caption
		}
		return label
	}

	switch m.Type {
	case "text":
		if m.Text != nil {
			return m.Text.Body
		}
	case "image":
		return withCaption("📷 Image", m.Image)
	case "video":
		return withCaption("🎬 Video", m.Video)
	case "audio":
		return "🎵 Audio"
	case "sticker":
		return "🏷️ Sticker"
	case "document":
		label := "📄 Document"
		if m.Document != nil && m.Document.Filename != "" {
			label += " (" + m.Document.Filename + ")"
		}
		return withCaption(label, m.Document)
	case "location":
		if l := m.Location; l != nil {
			parts := []string{"📍 Location"}
			if l.Name != "" {
				parts = append(parts, l.Name)
			}
			if l.Address != "" {
				parts = append(parts, l.Address)
			}
			return fmt.Sprintf("%s\nhttps://maps.google.com/?q=%f,%f",
				strings.Join(parts, " — "), l.Latitude, l.Longitude)
		}
	case "reaction":
		if m.Reaction != nil {
			if m.Reaction.Emoji == "" {
				return "Removed a reaction"
			}
			return "Reacted " + m.Reaction.Emoji
		}
	case "button":
		if m.Button != nil {
			return m.Button.Text
		}
	case "interactive":
		if i := m.Interactive; i != nil {
			if i.ButtonReply != nil {
				return i.ButtonReply.Title
			}
			if i.ListReply != nil {
				return i.ListReply.Title
			}
		}
	case "contacts":
		names := make([]string, 0, len(m.Contacts))
		for _, c := range m.Contacts {
			names = append(names, c.Name.FormattedName)
		}
		return "👤 Shared contact: " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("_[unsupported message type: %s]_", m.Type)
}

// dedupe remembers recently forwarded message IDs so Meta's redeliveries
// don't post the same message twice. It is in-memory only.
type dedupe struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newDedupe(ttl time.Duration) *dedupe {
	return &dedupe{ttl: ttl, seen: make(map[string]time.Time)}
}

func (d *dedupe) has(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.seen[id]
	return ok && time.Since(t) < d.ttl
}

func (d *dedupe) add(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for k, t := range d.seen {
		if now.Sub(t) >= d.ttl {
			delete(d.seen, k)
		}
	}
	d.seen[id] = now
}
