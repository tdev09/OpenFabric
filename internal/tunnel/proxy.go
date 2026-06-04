package tunnel

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ReverseProxy listens on the WireGuard tunnel IP and proxies requests
// to the local dashboard (localhost:4892).
type ReverseProxy struct {
	localPort int
	server    *http.Server
	pin       *PINManager
	log       *zap.Logger
}

// NewReverseProxy creates a proxy that forwards to the given local port.
func NewReverseProxy(localPort int, log *zap.Logger) *ReverseProxy {
	return &ReverseProxy{
		localPort: localPort,
		log:       log,
	}
}

// SetPINManager configures the PIN manager.
func (p *ReverseProxy) SetPINManager(pin *PINManager) {
	p.pin = pin
}

// Start begins listening on the tunnel IP address on port 4892.
// Only requests from within the 10.8.0.0/24 WireGuard network are accepted,
// unless running in relay-only mode (bound to 127.0.0.1).
func (p *ReverseProxy) Start(tunnelIP string) error {
	host := strings.Split(tunnelIP, "/")[0]
	listenAddr := fmt.Sprintf("%s:%d", host, 4894)

	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", p.localPort))
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Inject X-Tunnel-Proxy header so the API server's CORSMiddleware trusts
	// requests forwarded through the WireGuard tunnel proxy.
	originalDirector := proxy.Director
	proxy.Director = func(outReq *http.Request) {
		originalDirector(outReq)
		outReq.Header.Set("X-Tunnel-Proxy", "1")
	}

	mux := http.NewServeMux()

	// In relay-only mode (localhost), skip the WireGuard subnet filter since
	// traffic arrives from the embedded relay, not from a WG interface.
	var handler http.Handler = proxy
	if host != "127.0.0.1" {
		handler = allowedSubnetOnly("10.8.0.0/24", proxy)
	}
	
	proxyLogger := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.log.Info("tunnel reverse proxy received request", zap.String("method", r.Method), zap.String("path", r.URL.Path))
		p.pin.Middleware(handler).ServeHTTP(w, r)
	})
	mux.Handle("/", proxyLogger)


	p.server = &http.Server{
		Addr:         listenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("proxy listen on %s: %w", listenAddr, err)
	}

	p.log.Info("tunnel reverse proxy listening", zap.String("addr", listenAddr))
	go func() {
		if errServe := p.server.Serve(ln); errServe != nil && errServe != http.ErrServerClosed {
			p.log.Error("tunnel proxy serve error", zap.Error(errServe))
		}
	}()
	return nil
}

// Stop gracefully shuts down the reverse proxy.
func (p *ReverseProxy) Stop() {
	if p.server != nil {
		p.server.Close()
	}
}

// allowedSubnetOnly is middleware that rejects requests from IPs outside the CIDR block.
func allowedSubnetOnly(cidr string, next http.Handler) http.Handler {
	_, network, _ := net.ParseCIDR(cidr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !network.Contains(ip) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
