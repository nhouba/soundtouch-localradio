// Package ui serves the installer's local web page and its JSON API.
package ui

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nhouba/soundtouch-localradio/internal/flow"
	"github.com/nhouba/soundtouch-localradio/internal/i18n"
	"github.com/nhouba/soundtouch-localradio/internal/speaker"
)

//go:embed index.html
var indexHTML []byte

// Options configures the UI server.
type Options struct {
	Runner     *flow.Runner
	Client     *speaker.Client // for /info of manually added speakers
	ScanClient *speaker.Client // short timeout, for discovery
	AutoScan   bool            // the page starts a network search when opened
	Subnets    func() []net.IP // defaults to speaker.LocalSubnets
	Quit       func()          // called by POST /api/quit
	Logf       func(format string, args ...any)
}

type entry struct {
	speaker.Speaker
	Checks flow.Checks `json:"checks"`
}

type stepState struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type job struct {
	Action string // install | uninstall
	IP     string
	Name   string
	State  string // running | done | error
	Steps  []stepState
	Err    *flow.UserError
	Result flow.Result
	cancel context.CancelFunc
}

// Server is the UI HTTP handler plus application state.
type Server struct {
	opt  Options
	addr string // 127.0.0.1:port, for the Host check

	mu          sync.Mutex
	speakers    []entry
	discovering bool
	scanned     bool
	job         *job
}

// New creates the server. addr is the address the UI is served on.
func New(opt Options, addr string) *Server {
	if opt.Subnets == nil {
		opt.Subnets = speaker.LocalSubnets
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	return &Server{opt: opt, addr: addr}
}

// ServeHTTP routes requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only answer requests addressed to us by IP/localhost (DNS rebinding).
	host := r.Host
	_, port, _ := net.SplitHostPort(s.addr)
	if host != s.addr && host != "localhost:"+port {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	if r.Method == http.MethodPost {
		// A custom header cannot be sent cross-origin without a CORS
		// preflight, which we never allow: blocks CSRF from other sites.
		if r.Header.Get("X-Installer") != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	lang := i18n.Pick(r.Header.Get("Accept-Language"))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Write(indexHTML)
	case r.Method == http.MethodGet && r.URL.Path == "/api/strings":
		writeJSON(w, map[string]any{"lang": lang, "strings": i18n.All(lang)})
	case r.Method == http.MethodGet && r.URL.Path == "/api/status":
		writeJSON(w, s.status(lang))
	case r.Method == http.MethodPost && r.URL.Path == "/api/discover":
		s.startDiscovery()
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/add":
		s.handleAdd(w, r, lang)
	case r.Method == http.MethodPost && (r.URL.Path == "/api/install" || r.URL.Path == "/api/uninstall"):
		s.handleStart(w, r, lang, strings.TrimPrefix(r.URL.Path, "/api/"))
	case r.Method == http.MethodPost && r.URL.Path == "/api/reset":
		s.mu.Lock()
		if s.job != nil && s.job.State != "running" {
			s.job = nil
		}
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodPost && r.URL.Path == "/api/quit":
		s.mu.Lock()
		busy := s.job != nil && s.job.State == "running"
		s.mu.Unlock()
		if busy {
			writeJSON(w, map[string]any{"error": errJSON(lang, &flow.UserError{Key: "busy"})})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		if s.opt.Quit != nil {
			go func() { time.Sleep(300 * time.Millisecond); s.opt.Quit() }()
		}
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

type errorJSON struct {
	Text   string `json:"text"`
	Next   string `json:"next"`
	Detail string `json:"detail,omitempty"`
}

func errJSON(lang string, e *flow.UserError) *errorJSON {
	if e == nil {
		return nil
	}
	out := &errorJSON{
		Text: i18n.T(lang, "err."+e.Key+".text", e.Args...),
		Next: i18n.T(lang, "err."+e.Key+".next"),
	}
	if e.Detail != nil {
		out.Detail = e.Detail.Error()
	}
	return out
}

func (s *Server) status(lang string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := map[string]any{
		"speakers":    append([]entry{}, s.speakers...),
		"discovering": s.discovering,
		"scanned":     s.scanned,
		"autoScan":    s.opt.AutoScan,
	}
	if j := s.job; j != nil {
		st["job"] = map[string]any{
			"action": j.Action,
			"ip":     j.IP,
			"name":   j.Name,
			"state":  j.State,
			"steps":  append([]stepState{}, j.Steps...),
			"error":  errJSON(lang, j.Err),
			"result": j.Result,
		}
	}
	return st
}

// upsert adds or replaces a speaker (keyed by IP).
func (s *Server) upsert(e entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.speakers {
		if s.speakers[i].IP == e.IP {
			s.speakers[i] = e
			return
		}
	}
	s.speakers = append(s.speakers, e)
}

func (s *Server) lookup(ip string) (entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.speakers {
		if e.IP == ip {
			return e, true
		}
	}
	return entry{}, false
}

func (s *Server) startDiscovery() {
	s.mu.Lock()
	if s.discovering {
		s.mu.Unlock()
		return
	}
	s.discovering = true
	s.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		hosts := speaker.Hosts24(s.opt.Subnets())
		s.opt.Logf("searching %d addresses for speakers", len(hosts))
		s.opt.ScanClient.Scan(ctx, hosts, 64, func(sp speaker.Speaker) {
			s.upsert(entry{Speaker: sp, Checks: s.opt.Runner.Preflight(ctx, sp)})
			s.opt.Logf("found %s (%s) at %s", sp.Name, sp.Type, sp.IP)
		})
		s.mu.Lock()
		s.discovering = false
		s.scanned = true
		s.mu.Unlock()
	}()
}

// AddSpeaker queries ip and adds it to the list (used for --speaker).
func (s *Server) AddSpeaker(ctx context.Context, ip string) (*entry, *flow.UserError) {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || parsed.To4() == nil {
		return nil, &flow.UserError{Key: "badip", Args: []any{ip}}
	}
	ip = parsed.To4().String()
	sp, err := s.opt.Client.Info(ctx, ip)
	if err != nil {
		return nil, &flow.UserError{Key: "info", Args: []any{ip}, Detail: err}
	}
	e := entry{Speaker: sp, Checks: s.opt.Runner.Preflight(ctx, sp)}
	s.upsert(e)
	return &e, nil
}

type ipRequest struct {
	IP string `json:"ip"`
}

func readIP(w http.ResponseWriter, r *http.Request) string {
	var req ipRequest
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	return strings.TrimSpace(req.IP)
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request, lang string) {
	ip := readIP(w, r)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	e, uerr := s.AddSpeaker(ctx, ip)
	if uerr != nil {
		writeJSON(w, map[string]any{"error": errJSON(lang, uerr)})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "speaker": e})
}

// progress forwards flow updates into the job state.
type progress struct {
	s *Server
	j *job
}

func (p progress) Step(id, state string) {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	for i := range p.j.Steps {
		if p.j.Steps[i].ID == id {
			p.j.Steps[i].State = state
		}
	}
}

func (p progress) Log(msg string) { p.s.opt.Logf("%s", msg) }

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request, lang, action string) {
	ip := readIP(w, r)
	e, ok := s.lookup(ip)
	if !ok {
		writeJSON(w, map[string]any{"error": errJSON(lang, &flow.UserError{Key: "info", Args: []any{ip}})})
		return
	}
	if action == "install" && !e.Checks.CanInstall() {
		writeJSON(w, map[string]any{"error": errJSON(lang, &flow.UserError{Key: "blocked"})})
		return
	}
	if action == "uninstall" && !e.Checks.ConsoleOK {
		writeJSON(w, map[string]any{"error": errJSON(lang, &flow.UserError{Key: "console"})})
		return
	}
	steps := flow.InstallSteps
	if action == "uninstall" {
		steps = flow.UninstallSteps
	}
	s.mu.Lock()
	if s.job != nil && s.job.State == "running" {
		s.mu.Unlock()
		writeJSON(w, map[string]any{"error": errJSON(lang, &flow.UserError{Key: "busy"})})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{Action: action, IP: e.IP, Name: e.Name, State: "running", cancel: cancel}
	for _, id := range steps {
		j.Steps = append(j.Steps, stepState{ID: id, State: flow.Pending})
	}
	s.job = j
	s.mu.Unlock()

	go func() {
		defer cancel()
		var res flow.Result
		var err error
		s.opt.Logf("%s on %s (%s) started", action, e.Name, e.IP)
		if action == "install" {
			res, err = s.opt.Runner.Install(ctx, e.IP, progress{s, j})
		} else {
			err = s.opt.Runner.Uninstall(ctx, e.IP, progress{s, j})
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		j.Result = res
		if err != nil {
			var ue *flow.UserError
			if !errors.As(err, &ue) {
				ue = &flow.UserError{Key: "internal", Detail: err}
			}
			j.Err = ue
			j.State = "error"
			s.opt.Logf("%s failed: %v", action, err)
			return
		}
		j.State = "done"
		s.opt.Logf("%s finished", action)
	}()
	writeJSON(w, map[string]any{"ok": true})
}

// Shutdown cancels a running job.
func (s *Server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil && s.job.cancel != nil {
		s.job.cancel()
	}
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
