package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
)

// Rejection says why a request does not carry a valid Slack signature, or "" when it does. It never
// contains the secret.
func Rejection(secret string, h http.Header, body []byte, now float64) string {
	ts, sig := h.Get("X-Slack-Request-Timestamp"), h.Get("X-Slack-Signature")
	if secret == "" {
		return "no signing secret configured"
	}
	at, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return "missing X-Slack-Request-Timestamp or X-Slack-Signature"
	}
	if off := int64(now) - at; off > 300 || off < -300 {
		return fmt.Sprintf("timestamp %ds off", off)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return fmt.Sprintf("signature mismatch over %d bytes: wrong signing secret, or the body changed in transit", len(body))
	}
	return ""
}

// Sign is the signature Slack would send for body at ts; for tests and local tools.
func Sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}
