package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run the real startup path in a child so the test covers env configuration
// and its wiring to outgoing HTTP content, without prescribing new fields.
func TestMessagePrefixProcess(t *testing.T) {
	if os.Getenv("WHATSAPP2DISCORD_TEST_PROCESS") == "1" {
		main()
	}
}

func TestMessagePrefix(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"unset", nil, "**Alice (+15551234567)**\nhello @everyone"},
		{"empty", []string{"MESSAGE_PREFIX="}, "**Alice (+15551234567)**\nhello @everyone"},
		{"configured_verbatim", []string{"MESSAGE_PREFIX=  [WhatsApp 📱]\n "}, "  [WhatsApp 📱]\n **Alice (+15551234567)**\nhello @everyone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := make(chan string, 1)
			discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message struct {
					Content string `json:"content"`
				}
				if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				contents <- message.Content
				w.WriteHeader(http.StatusNoContent)
			}))
			defer discord.Close()

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestMessagePrefixProcess$")
			for _, env := range os.Environ() {
				key, _, _ := strings.Cut(env, "=")
				switch key {
				case "ADDR", "WHATSAPP_VERIFY_TOKEN", "WHATSAPP_APP_SECRET", "DISCORD_WEBHOOK_URL", "MESSAGE_PREFIX", "WHATSAPP2DISCORD_TEST_PROCESS":
				default:
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "WHATSAPP2DISCORD_TEST_PROCESS=1", "ADDR="+addr,
				"WHATSAPP_VERIFY_TOKEN=verify-me", "WHATSAPP_APP_SECRET="+testSecret, "DISCORD_WEBHOOK_URL="+discord.URL)
			cmd.Env = append(cmd.Env, tc.env...)
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cmd.Process.Kill()
				cmd.Wait()
			})

			client := &http.Client{Timeout: time.Second}
			url := "http://" + addr
			deadline := time.Now().Add(5 * time.Second)
			for {
				resp, err := client.Get(url + "/healthz")
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("app did not become ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			req, err := http.NewRequest(http.MethodPost, url+"/webhook", strings.NewReader(textPayload))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Hub-Signature-256", sign(textPayload))
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("webhook status = %d, want 200", resp.StatusCode)
			}
			select {
			case got := <-contents:
				if got != tc.want {
					t.Errorf("Discord content = %q, want %q", got, tc.want)
				}
			case <-time.After(time.Second):
				t.Fatal("no outgoing Discord message")
			}
		})
	}
}
