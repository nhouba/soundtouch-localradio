// Package fileserver is the temporary HTTP server the speaker downloads the
// station files from and uploads its backup to during installation.
package fileserver

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Served are the only files the speaker may download (paths below /f/).
var Served = []string{"registry.json", "index.html", "cgi-bin/s", "cgi-bin/api"}

// Upload size limits per accepted name.
var uploadLimits = map[string]int64{
	"speaker-nv.tar": 64 << 20,
	"done.txt":       4 << 20,
}

// Server serves files and accepts uploads.
type Server struct {
	files     fs.FS
	backupDir string

	mu       sync.Mutex
	received map[string]int64
	firstGet time.Time // first file download since the last ResetFirstDownload
	done     chan struct{}
	doneOnce sync.Once
	log      func(string)

	srv *http.Server
	ln  net.Listener
}

// New creates a server for files (rooted like the speaker/ directory) that
// stores uploads in backupDir. log may be nil.
func New(files fs.FS, backupDir string, log func(string)) *Server {
	if log == nil {
		log = func(string) {}
	}
	return &Server{files: files, backupDir: backupDir, received: map[string]int64{}, done: make(chan struct{}), log: log}
}

// Start listens on addr (e.g. "0.0.0.0:0") and serves in the background.
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 20 * time.Second}
	go s.srv.Serve(ln)
	return nil
}

// Port returns the listening port.
func (s *Server) Port() int {
	return s.ln.Addr().(*net.TCPAddr).Port
}

// Close stops the server.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// Done is closed when done.txt has been uploaded.
func (s *Server) Done() <-chan struct{} { return s.done }

// Received returns the size of an uploaded file, or -1 if not received.
func (s *Server) Received(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.received[name]; ok {
		return n
	}
	return -1
}

// FirstDownload returns when the speaker first downloaded a file (zero if not yet).
func (s *Server) FirstDownload() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstGet
}

// ResetFirstDownload forgets the first download time (before retrying a reboot).
func (s *Server) ResetFirstDownload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firstGet = time.Time{}
}

// ServeHTTP implements GET /f/<file> and PUT /up/<name>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.HasPrefix(p, "/f/"):
		s.serveFile(w, r, strings.TrimPrefix(p, "/f/"))
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/up/"):
		s.upload(w, r, strings.TrimPrefix(p, "/up/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	allowed := false
	for _, f := range Served {
		if name == f {
			allowed = true
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.log("speaker downloaded " + name)
	s.mu.Lock()
	if s.firstGet.IsZero() {
		s.firstGet = time.Now()
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		w.Write(data)
	}
}

// upload handles both Content-Length and chunked bodies (the speaker's
// "curl -T -" uses Transfer-Encoding: chunked; net/http decodes it).
func (s *Server) upload(w http.ResponseWriter, r *http.Request, name string) {
	limit, ok := uploadLimits[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.save(name, http.MaxBytesReader(w, r.Body, limit)); err != nil {
		s.log("upload of " + name + " failed: " + err.Error())
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
	if name == "done.txt" {
		s.doneOnce.Do(func() { close(s.done) })
	}
}

func (s *Server) save(name string, body io.Reader) error {
	final := filepath.Join(s.backupDir, name)
	tmp, err := os.CreateTemp(s.backupDir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename
	if err := tmp.Chmod(0o600); err != nil && !isUnsupported(err) {
		tmp.Close()
		return err
	}
	n, err := io.Copy(tmp, body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return err
	}
	os.Chmod(final, 0o600)
	s.mu.Lock()
	s.received[name] = n
	s.mu.Unlock()
	s.log("received " + name + " (" + strconv.FormatInt(n, 10) + " bytes)")
	return nil
}

// isUnsupported reports chmod errors on platforms without Unix permissions.
func isUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}
