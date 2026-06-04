package tunnel

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

const (
	pinLength       = 6
	pinCookieName   = "fabric_tunnel_pin"
	pinCookieMaxAge = 24 * 60 * 60 // 24 hours in seconds
	pinBcryptCost   = 12
)

// PINManager generates, validates, and enforces the browser PIN that protects
// the dashboard when accessed remotely via the tunnel.
type PINManager struct {
	mu          sync.RWMutex
	cfg         *TunnelConfig
	sessionKeys map[string]time.Time // token -> expiry
	log         *zap.Logger
	submitLim   map[string]int // ip -> login attempts (rate limiting)
	limMu       sync.Mutex
}

// NewPINManager creates a PIN manager backed by the tunnel config.
func NewPINManager(cfg *TunnelConfig, log *zap.Logger) *PINManager {
	return &PINManager{
		cfg:         cfg,
		sessionKeys: make(map[string]time.Time),
		log:         log,
		submitLim:   make(map[string]int),
	}
}

// GeneratePIN creates a new 6-digit PIN, stores its bcrypt hash, and returns
// the plaintext PIN to display once to the user.
func (p *PINManager) GeneratePIN() (string, error) {
	digits := make([]byte, pinLength)
	if _, err := rand.Read(digits); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}

	pin := ""
	for _, b := range digits {
		pin += fmt.Sprintf("%d", b%10)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(pin), pinBcryptCost)
	if err != nil {
		return "", fmt.Errorf("bcrypt: %w", err)
	}

	p.mu.Lock()
	p.cfg.PINHash = string(hash)
	p.cfg.PINEnabled = true
	p.mu.Unlock()

	p.log.Info("generated new browser tunnel PIN")
	return pin, nil
}

// RevokePIN disables PIN authentication and clears all active sessions.
func (p *PINManager) RevokePIN() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.PINHash = ""
	p.cfg.PINEnabled = false
	p.sessionKeys = make(map[string]time.Time)
	p.log.Info("revoked browser tunnel PIN and cleared active sessions")
}

// ValidatePIN checks a submitted PIN against the stored bcrypt hash.
// Returns a session token on success.
func (p *PINManager) ValidatePIN(pin string) (string, error) {
	p.mu.RLock()
	hash := p.cfg.PINHash
	p.mu.RUnlock()

	if hash == "" {
		return "", fmt.Errorf("PIN authentication not configured")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)); err != nil {
		return "", fmt.Errorf("invalid PIN")
	}

	// Generate a secure session token
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("token generate: %w", err)
	}
	token := hex.EncodeToString(raw)

	p.mu.Lock()
	p.sessionKeys[token] = time.Now().Add(pinCookieMaxAge * time.Second)
	p.mu.Unlock()
	return token, nil
}

// Middleware wraps an HTTP handler with PIN authentication.
func (p *PINManager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.RLock()
		enabled := p.cfg.PINEnabled
		p.mu.RUnlock()

		if !enabled {
			next.ServeHTTP(w, r)
			return
		}

		// PIN submission endpoint
		if r.Method == http.MethodPost && r.URL.Path == "/_fabric/pin" {
			p.handlePINSubmit(w, r)
			return
		}

		// Check session cookie
		cookie, err := r.Cookie(pinCookieName)
		if err == nil && p.validSession(cookie.Value) {
			next.ServeHTTP(w, r)
			return
		}

		// Serve PIN entry page
		p.servePINPage(w, r)
	})
}

// handlePINSubmit processes PIN form submissions with simple rate-limiting protection.
func (p *PINManager) handlePINSubmit(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = host
	}
	p.limMu.Lock()
	attempts := p.submitLim[ip]
	if attempts >= 5 {
		p.limMu.Unlock()
		p.log.Warn("brute force attempt blocked on PIN validation", zap.String("ip", ip))
		http.Error(w, "Too many failed attempts. Try again later.", http.StatusTooManyRequests)
		return
	}
	p.limMu.Unlock()

	pin := r.FormValue("pin")
	token, err := p.ValidatePIN(pin)
	if err != nil {
		p.log.Warn("PIN validation failed", zap.Error(err), zap.Int("pin_len", len(pin)))
		p.limMu.Lock()
		p.submitLim[ip]++
		p.limMu.Unlock()

		// reset limit after 1 minute
		go func(clientIP string) {
			time.Sleep(1 * time.Minute)
			p.limMu.Lock()
			if p.submitLim[clientIP] > 0 {
				p.submitLim[clientIP]--
			}
			p.limMu.Unlock()
		}(ip)

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusUnauthorized)
		html := strings.Replace(pinPageHTMLWithError, "%s", "Incorrect 6-digit PIN. Please try again.", 1)
		fmt.Fprint(w, html)
		return
	}

	p.limMu.Lock()
	delete(p.submitLim, ip) // reset limit on success
	p.limMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     pinCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   pinCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false, // WireGuard tunnel itself is encrypted
	})

	// Redirect back to request page
	ref := r.Referer()
	if ref == "" || stringsContains(ref, "/_fabric/pin") {
		ref = "/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}

func stringsContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// validSession checks if a session token is present and not expired.
func (p *PINManager) validSession(token string) bool {
	p.mu.RLock()
	expiry, ok := p.sessionKeys[token]
	p.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		p.mu.Lock()
		delete(p.sessionKeys, token)
		p.mu.Unlock()
		return false
	}
	// Constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare([]byte(token), []byte(token)) == 1
}

// servePINPage writes a minimal PIN entry HTML page.
func (p *PINManager) servePINPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(w, pinPageHTML)
}

const pinPageHTML = `<!DOCTYPE html>
<html>
<head>
  <title>OpenFabric - Remote Access</title>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { background: #0D1117; color: #A8B2C1; font-family: -apple-system, BlinkMacSystemFont, 'Inter', sans-serif;
           display: flex; align-items: center; justify-content: center; min-height: 100vh; }
    .card { background: #161B22; border: 1px solid #30363D; border-radius: 16px;
            padding: 40px 36px; text-align: center; width: 360px;
            box-shadow: 0 8px 32px rgba(0,0,0,0.6); }
    .logo { width: 40px; height: 40px; margin: 0 auto 20px;
            background: rgba(0,201,167,0.15); border-radius: 50%;
            display: flex; align-items: center; justify-content: center; }
    .logo svg { width: 22px; height: 22px; }
    h2 { color: #E6EDF3; margin: 0 0 8px; font-weight: 700; font-size: 22px; }
    .subtitle { color: #8B949E; font-size: 14px; margin: 0 0 28px; line-height: 1.5; }
    .hint { background: rgba(0,201,167,0.08); border: 1px solid rgba(0,201,167,0.2);
            border-radius: 8px; padding: 10px 14px; font-size: 12px; color: #00C9A7;
            margin-bottom: 24px; line-height: 1.5; text-align: left; }
    .hint strong { display: block; margin-bottom: 2px; }
    input { width: 100%; padding: 14px; background: #0D1117; border: 1.5px solid #30363D;
            border-radius: 10px; color: #E6EDF3; font-size: 28px; letter-spacing: 12px;
            text-align: center; margin-bottom: 16px; outline: none;
            font-family: 'Courier New', monospace; transition: border-color 0.2s; }
    input:focus { border-color: #00C9A7; box-shadow: 0 0 0 3px rgba(0,201,167,0.15); }
    input::placeholder { letter-spacing: 6px; font-size: 18px; color: #444d56; }
    button { width: 100%; padding: 13px; background: #00C9A7; border: none;
             border-radius: 10px; color: #0D1117; font-weight: 700; font-size: 15px;
             cursor: pointer; transition: background 0.2s; letter-spacing: 0.02em; }
    button:hover { background: #00e0bc; }
    .footer { margin-top: 20px; font-size: 12px; color: #4A5568; }
  </style>
</head>
<body>
  <div class="card">
    <div class="logo">
      <svg viewBox="0 0 28 28" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="14" cy="14" r="13" stroke="#00C9A7" stroke-width="1.5"/>
        <circle cx="14" cy="7" r="2.5" fill="#00C9A7"/>
        <circle cx="21" cy="19" r="2.5" fill="#00C9A7"/>
        <circle cx="7" cy="19" r="2.5" fill="#00C9A7"/>
        <line x1="14" y1="7" x2="21" y2="19" stroke="#00C9A7" stroke-width="1.2" stroke-opacity="0.5"/>
        <line x1="14" y1="7" x2="7" y2="19" stroke="#00C9A7" stroke-width="1.2" stroke-opacity="0.5"/>
        <line x1="21" y1="19" x2="7" y2="19" stroke="#00C9A7" stroke-width="1.2" stroke-opacity="0.5"/>
      </svg>
    </div>
    <h2>OpenFabric</h2>
    <p class="subtitle">Enter your 6-digit PIN to access the cluster dashboard remotely.</p>
    <div class="hint">
      <strong>Where is my PIN?</strong>
      Open the Fabric Tunnel settings on your host machine &rarr; click <em>Regenerate PIN</em> &rarr; copy the 6-digit code shown.
    </div>
    <form method="POST" action="/_fabric/pin">
      <input type="password" name="pin" maxlength="6" inputmode="numeric"
             pattern="[0-9]*" autofocus placeholder="------"
             autocomplete="one-time-code">
      <button type="submit">Unlock Dashboard</button>
    </form>
    <p class="footer">Protected by OpenFabric Tunnel &mdash; PIN valid for 24 hours</p>
  </div>
</body>
</html>`

const pinPageHTMLWithError = `<!DOCTYPE html>
<html>
<head>
  <title>OpenFabric - Remote Access</title>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body { background: #0D1117; color: #A8B2C1; font-family: -apple-system, BlinkMacSystemFont, 'Inter', sans-serif;
           display: flex; align-items: center; justify-content: center; min-height: 100vh; }
    .card { background: #161B22; border: 1px solid #30363D; border-radius: 16px;
            padding: 40px 36px; text-align: center; width: 360px;
            box-shadow: 0 8px 32px rgba(0,0,0,0.6); }
    .logo { width: 40px; height: 40px; margin: 0 auto 20px;
            background: rgba(0,201,167,0.15); border-radius: 50%;
            display: flex; align-items: center; justify-content: center; }
    .logo svg { width: 22px; height: 22px; }
    h2 { color: #E6EDF3; margin: 0 0 8px; font-weight: 700; font-size: 22px; }
    .subtitle { color: #8B949E; font-size: 14px; margin: 0 0 20px; line-height: 1.5; }
    .hint { background: rgba(0,201,167,0.08); border: 1px solid rgba(0,201,167,0.2);
            border-radius: 8px; padding: 10px 14px; font-size: 12px; color: #00C9A7;
            margin-bottom: 20px; line-height: 1.5; text-align: left; }
    .hint strong { display: block; margin-bottom: 2px; }
    input { width: 100%; padding: 14px; background: #0D1117; border: 1.5px solid #F85149;
            border-radius: 10px; color: #E6EDF3; font-size: 28px; letter-spacing: 12px;
            text-align: center; margin-bottom: 12px; outline: none;
            font-family: 'Courier New', monospace; }
    input::placeholder { letter-spacing: 6px; font-size: 18px; color: #444d56; }
    button { width: 100%; padding: 13px; background: #00C9A7; border: none;
             border-radius: 10px; color: #0D1117; font-weight: 700; font-size: 15px;
             cursor: pointer; transition: background 0.2s; }
    button:hover { background: #00e0bc; }
    .error { color: #F85149; font-size: 13px; margin-bottom: 14px;
             background: rgba(248,81,73,0.1); border: 1px solid rgba(248,81,73,0.3);
             border-radius: 8px; padding: 8px 12px; }
    .footer { margin-top: 20px; font-size: 12px; color: #4A5568; }
  </style>
</head>
<body>
  <div class="card">
    <div class="logo">
      <svg viewBox="0 0 28 28" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="14" cy="14" r="13" stroke="#00C9A7" stroke-width="1.5"/>
        <circle cx="14" cy="7" r="2.5" fill="#00C9A7"/>
        <circle cx="21" cy="19" r="2.5" fill="#00C9A7"/>
        <circle cx="7" cy="19" r="2.5" fill="#00C9A7"/>
        <line x1="14" y1="7" x2="21" y2="19" stroke="#00C9A7" stroke-width="1.2" stroke-opacity="0.5"/>
        <line x1="14" y1="7" x2="7" y2="19" stroke="#00C9A7" stroke-width="1.2" stroke-opacity="0.5"/>
        <line x1="21" y1="19" x2="7" y2="19" stroke="#00C9A7" stroke-width="1.2" stroke-opacity="0.5"/>
      </svg>
    </div>
    <h2>OpenFabric</h2>
    <p class="subtitle">Enter your 6-digit PIN to access the cluster dashboard remotely.</p>
    <div class="error">%s</div>
    <div class="hint">
      <strong>Where is my PIN?</strong>
      Open the Fabric Tunnel settings on your host machine &rarr; click <em>Regenerate PIN</em> &rarr; copy the 6-digit code shown.
    </div>
    <form method="POST" action="/_fabric/pin">
      <input type="password" name="pin" maxlength="6" inputmode="numeric"
             pattern="[0-9]*" autofocus placeholder="------"
             autocomplete="one-time-code">
      <button type="submit">Try Again</button>
    </form>
    <p class="footer">Protected by OpenFabric Tunnel &mdash; PIN valid for 24 hours</p>
  </div>
</body>
</html>`
