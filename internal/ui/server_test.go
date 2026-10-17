package ui

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nhouba/soundtouch-localradio/internal/flow"
	"github.com/nhouba/soundtouch-localradio/internal/speaker"
)

func newTestServer() *Server {
	c := &speaker.Client{Ports: speaker.Ports{API: 1, Console: 1, Page: 1}, HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
	return New(Options{
		Runner:     flow.New(flow.Config{Client: c}),
		Client:     c,
		ScanClient: c,
		AutoScan:   true,
		Subnets:    func() []net.IP { return nil }, // never scan a real network in tests
	}, "127.0.0.1:5555")
}

func do(s *Server, method, path, host, body string, hdr bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if hdr {
		r.Header.Set("X-Installer", "1")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestPageAndHostCheck(t *testing.T) {
	s := newTestServer()
	w := do(s, "GET", "/", "127.0.0.1:5555", "", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>SoundTouch Radio Installer</title>") {
		t.Fatalf("index: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "http://") || strings.Contains(w.Body.String(), "https://") {
		t.Error("page references external resources")
	}
	if do(s, "GET", "/", "localhost:5555", "", false).Code != 200 {
		t.Error("localhost rejected")
	}
	if do(s, "GET", "/", "evil.example:5555", "", false).Code != 403 {
		t.Error("foreign Host accepted")
	}
	if do(s, "POST", "/api/discover", "127.0.0.1:5555", "{}", false).Code != 403 {
		t.Error("POST without X-Installer accepted")
	}
}

func TestStringsAndBadIP(t *testing.T) {
	s := newTestServer()
	w := do(s, "GET", "/api/strings", "127.0.0.1:5555", "", false)
	var st struct {
		Strings map[string]string
	}
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Strings["spk.install"] != "Install" {
		t.Errorf("strings: %s", w.Body.String())
	}
	w = do(s, "POST", "/api/add", "127.0.0.1:5555", `{"ip":"not-an-ip"}`, true)
	if !strings.Contains(w.Body.String(), "not a valid IP address") {
		t.Errorf("add: %s", w.Body.String())
	}
	w = do(s, "POST", "/api/install", "127.0.0.1:5555", `{"ip":"10.9.9.9"}`, true)
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("install of unknown speaker: %s", w.Body.String())
	}
}

func TestDiscoveryWithoutNetwork(t *testing.T) {
	s := newTestServer()
	do(s, "POST", "/api/discover", "127.0.0.1:5555", "{}", true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w := do(s, "GET", "/api/status", "127.0.0.1:5555", "", false)
		if strings.Contains(w.Body.String(), `"scanned":true`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("discovery did not finish")
}
