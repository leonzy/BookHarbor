package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const accessCapabilityPath = "/_internal/access-capability"

func accessCapabilityProof(secret, challenge string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte("bookharbor-acl-v1:" + challenge))
	return hex.EncodeToString(h.Sum(nil))
}

// The probe is loopback-only and proves that the serving process has the ACL
// resolver attached. A legacy process forwarding unknown routes cannot pass:
// the probe disables proxy use and redirects, and forwarded probes are denied.
func (r *accessRuntime) capabilityProbe(secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		ip := net.ParseIP(host)
		challenge := request.Header.Get("X-BookHarbor-Access-Challenge")
		decoded, decodeErr := hex.DecodeString(challenge)
		if err != nil || ip == nil || !ip.IsLoopback() || request.Header.Get("X-Forwarded-For") != "" || request.Method != "GET" || decodeErr != nil || len(decoded) != 32 || secret == "" {
			http.NotFound(w, request)
			return
		}
		var minimum int
		if err = r.store.DB.QueryRowContext(request.Context(), `SELECT minimum_reader FROM acl_state WHERE id=1`).Scan(&minimum); err != nil || minimum > 1 {
			http.Error(w, "access runtime not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, accessCapabilityProof(secret, challenge))
	}
}

func (r *accessRuntime) checkCapabilities(secret, peers string) func(context.Context) error {
	return func(ctx context.Context) error {
		if _, err := r.visibilityCurrent(ctx); err != nil {
			return err
		}
		client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		for _, peer := range strings.Split(peers, ",") {
			peer = strings.TrimSpace(peer)
			if peer == "" {
				continue
			}
			u, err := url.Parse(peer)
			if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				return fmt.Errorf("invalid access peer")
			}
			u.Path = accessCapabilityPath
			nonce := make([]byte, 32)
			if _, err = rand.Read(nonce); err != nil {
				return err
			}
			challenge := hex.EncodeToString(nonce)
			req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
			if err != nil {
				return err
			}
			req.Header.Set("X-BookHarbor-Access-Challenge", challenge)
			response, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("access peer unavailable: %w", err)
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 129))
			response.Body.Close()
			if readErr != nil || response.StatusCode != http.StatusOK || !hmac.Equal(body, []byte(accessCapabilityProof(secret, challenge))) {
				return fmt.Errorf("access peer does not support current permissions")
			}
		}
		return nil
	}
}
