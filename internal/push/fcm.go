package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

// Notifier sends FCM push notifications via the HTTP v1 API.
// Set FIREBASE_SERVER_KEY to enable; if unset, notifications are logged only.
type Notifier struct {
	serverKey string
}

// NewNotifier builds a Notifier, reading FIREBASE_SERVER_KEY from env.
func NewNotifier() *Notifier {
	return &Notifier{serverKey: os.Getenv("FIREBASE_SERVER_KEY")}
}

type fcmMessage struct {
	To           string            `json:"to"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data,omitempty"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Send delivers a notification to a single FCM device token.
func (n *Notifier) Send(ctx context.Context, token, title, body string, data map[string]string) error {
	if n.serverKey == "" {
		slog.Info("push dev mode", "token", token[:min(8, len(token))], "title", title)
		return nil
	}

	msg := fcmMessage{
		To:           token,
		Notification: fcmNotification{Title: title, Body: body},
		Data:         data,
	}
	payload, _ := json.Marshal(msg)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://fcm.googleapis.com/fcm/send", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "key="+n.serverKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fcm: status %d", resp.StatusCode)
	}
	return nil
}

// SendToUser delivers a notification to all registered devices of a user.
func (n *Notifier) SendToUser(ctx context.Context, tokens []string, title, body string, data map[string]string) {
	for _, t := range tokens {
		if err := n.Send(ctx, t, title, body, data); err != nil {
			slog.Warn("push send failed", "err", err)
		}
	}
}

// min returns the smaller of a and b (used to safely truncate tokens in log output).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
