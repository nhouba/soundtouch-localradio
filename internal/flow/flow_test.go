package flow

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nhouba/soundtouch-localradio/internal/hook"
	"github.com/nhouba/soundtouch-localradio/internal/speaker"
)

const infoXML = `<?xml version="1.0" encoding="UTF-8" ?><info deviceID="A0B1C2D3E4F5"><name>Kitchen</name><type>SoundTouch 10</type><margeAccountUUID>1234567</margeAccountUUID><components><component><componentCategory>SCM</componentCategory><softwareVersion>27.0.3.4629.5041371 epdbuild.trunk.hepdswbld04.2022-08-04T11:20:29</softwareVersion><serialNumber>X</serialNumber></component></components></info>`

// fakeSpeaker emulates the speaker's API, console and boot hook on localhost.
type fakeSpeaker struct {
	t       *testing.T
	api     *httptest.Server
	page    *httptest.Server
	console net.Listener

	mu        sync.Mutex
	cmds      []string
	hook      string
	installed map[string]string
	pageUp    bool
	reject    bool
	noBoot    bool
	stalls    int // boots that stop after the first download (seen on a real SoundTouch 10)
	boots     int
}

func newFakeSpeaker(t *testing.T) *fakeSpeaker {
	f := &fakeSpeaker{t: t, installed: map[string]string{}}
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			io.WriteString(w, infoXML)
		case "/presets":
			io.WriteString(w, "<presets/>")
		case "/sources":
			io.WriteString(w, "<sources/>")
		default:
			http.NotFound(w, r)
		}
	}))
	f.page = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		up := f.pageUp
		f.mu.Unlock()
		if !up {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, f.installed["index.html"])
	}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.console = ln
	go f.serveConsole()
	t.Cleanup(func() { f.api.Close(); f.page.Close(); ln.Close() })
	return f
}

func port(t *testing.T, addr string) int {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(addr, "http://"))
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fakeSpeaker) ports() speaker.Ports {
	return speaker.Ports{API: port(f.t, f.api.URL), Console: port(f.t, f.console.Addr().String()), Page: port(f.t, f.page.URL)}
}

func (f *fakeSpeaker) serveConsole() {
	for {
		conn, err := f.console.Accept()
		if err != nil {
			return
		}
		go f.handleConsole(conn)
	}
}

var envswitchRe = regexp.MustCompile(`^envswitch boseurls set (.*)$`)

func (f *fakeSpeaker) handleConsole(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		f.mu.Lock()
		f.cmds = append(f.cmds, line)
		reject := f.reject
		f.mu.Unlock()
		if m := envswitchRe.FindStringSubmatch(line); m != nil && !reject {
			f.mu.Lock()
			f.hook = m[1]
			f.mu.Unlock()
			io.WriteString(conn, "->Setting Bose Server URLs to "+m[1]+"\r\nOK\r\n")
			continue
		}
		if line == "sys reboot" {
			go f.boot()
			return
		}
		io.WriteString(conn, "->OK\r\n")
	}
}

var srvRe = regexp.MustCompile(`http://127\.0\.0\.1:\d+`)

// boot simulates what the speaker's root shell does with the stored hook.
func (f *fakeSpeaker) boot() {
	f.mu.Lock()
	h := f.hook
	noBoot := f.noBoot
	f.boots++
	stall := f.stalls > 0
	if stall {
		f.stalls--
	}
	f.mu.Unlock()
	if noBoot || !strings.Contains(h, "/f/registry.json") {
		return
	}
	srv := srvRe.FindString(h)
	for _, name := range []string{"registry.json", "index.html", "cgi-bin/s", "cgi-bin/api"} {
		resp, err := http.Get(srv + "/f/" + name)
		if err != nil {
			f.t.Errorf("download %s: %v", name, err)
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		f.mu.Lock()
		f.installed[name] = string(b)
		f.mu.Unlock()
		if stall {
			return
		}
	}
	// curl -T - : unknown length => Transfer-Encoding: chunked
	for _, up := range []struct{ name, data string }{{"speaker-nv.tar", "TARDATA"}, {"done.txt", "listing"}} {
		pr, pw := io.Pipe()
		go func() { io.WriteString(pw, up.data[:3]); io.WriteString(pw, up.data[3:]); pw.Close() }()
		req, _ := http.NewRequest(http.MethodPut, srv+"/up/"+up.name, pr)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			f.t.Errorf("upload %s: %v", up.name, err)
			return
		}
		resp.Body.Close()
	}
	f.mu.Lock()
	f.pageUp = true
	f.mu.Unlock()
}

type recorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *recorder) Step(id, state string) {
	r.mu.Lock()
	r.steps = append(r.steps, id+":"+state)
	r.mu.Unlock()
}
func (r *recorder) Log(string) {}

var fastTiming = Timing{
	CmdDelay:      30 * time.Millisecond,
	UploadTimeout: 5 * time.Second,
	ConsoleWait:   2 * time.Second,
	ConsoleRetry:  50 * time.Millisecond,
	FinalWait:     10 * time.Millisecond,
	PageTimeout:   2 * time.Second,
	StallTimeout:  300 * time.Millisecond,
	MaxRestarts:   2,
}

func newRunner(f *fakeSpeaker, root string) *Runner {
	return New(Config{
		Client:     &speaker.Client{Ports: f.ports(), HTTP: &http.Client{Timeout: 2 * time.Second}},
		Files:      fstest.MapFS{"registry.json": {Data: []byte("{}")}, "index.html": {Data: []byte("<h1>page</h1>")}, "cgi-bin/s": {Data: []byte("s")}, "cgi-bin/api": {Data: []byte("api")}},
		BackupRoot: root,
		ListenAddr: "127.0.0.1:0",
		Timing:     fastTiming,
		Now:        func() time.Time { return time.Date(2026, 10, 17, 9, 8, 7, 0, time.UTC) },
	})
}

func TestInstallEndToEnd(t *testing.T) {
	f := newFakeSpeaker(t)
	root := filepath.Join(t.TempDir(), "SoundTouch-Backup")
	r := newRunner(f, root)
	rec := &recorder{}
	res, err := r.Install(context.Background(), "127.0.0.1", rec)
	if err != nil {
		t.Fatalf("install: %v (steps %v)", err, rec.steps)
	}
	if !res.PageUp || !res.TarReceived {
		t.Errorf("result %+v", res)
	}
	wantDir := filepath.Join(root, "A0B1C2D3E4F5-20261017-090807")
	if res.BackupDir != wantDir {
		t.Errorf("backup dir %s, want %s", res.BackupDir, wantDir)
	}
	for _, n := range []string{"info.xml", "presets.xml", "sources.xml", "speaker-nv.tar", "done.txt"} {
		if _, err := os.Stat(filepath.Join(wantDir, n)); err != nil {
			t.Errorf("missing %s: %v", n, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(wantDir, "speaker-nv.tar")); string(b) != "TARDATA" {
		t.Errorf("tar content %q", b)
	}
	if st, err := os.Stat(wantDir); err == nil && st.Mode().Perm() != 0o700 {
		t.Errorf("backup dir mode %v", st.Mode().Perm())
	}
	if f.installed["index.html"] != "<h1>page</h1>" || f.installed["cgi-bin/api"] != "api" {
		t.Errorf("installed %v", f.installed)
	}

	srv := srvRe.FindString(f.cmds[1])
	want := append(hook.InstallCommands(srv), hook.FinalizeCommands()...)
	if strings.Join(f.cmds, "\n") != strings.Join(want, "\n") {
		t.Errorf("console commands:\n%q\nwant\n%q", f.cmds, want)
	}
	if f.cmds[1] != `envswitch boseurls set ";`+hook.Install(srv)+` ;" ;` {
		t.Errorf("install hook command %q", f.cmds[1])
	}
	if f.hook != `";`+hook.Runtime()+` ;" ;` {
		t.Errorf("final stored hook %q", f.hook)
	}
	wantSteps := []string{}
	for _, s := range InstallSteps {
		wantSteps = append(wantSteps, s+":active", s+":done")
	}
	if strings.Join(rec.steps, ",") != strings.Join(wantSteps, ",") {
		t.Errorf("steps %v", rec.steps)
	}
}

func TestInstallRejectedDoesNotReboot(t *testing.T) {
	f := newFakeSpeaker(t)
	f.reject = true
	r := newRunner(f, t.TempDir())
	_, err := r.Install(context.Background(), "127.0.0.1", &recorder{})
	ue, ok := err.(*UserError)
	if !ok || ue.Key != "rejected" {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.cmds {
		if c == "sys reboot" {
			t.Error("rebooted although the hook was rejected")
		}
	}
}

func TestUninstallCommands(t *testing.T) {
	f := newFakeSpeaker(t)
	r := newRunner(f, t.TempDir())
	if err := r.Uninstall(context.Background(), "127.0.0.1", &recorder{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"envswitch boseurls set https://streaming.bose.com https://worldwide.bose.com/updates/soundtouch", "sys reboot"}
	if strings.Join(f.cmds, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q", f.cmds)
	}
}

func TestPreflight(t *testing.T) {
	f := newFakeSpeaker(t)
	r := newRunner(f, t.TempDir())
	s, err := speaker.ParseInfo("127.0.0.1", []byte(infoXML))
	if err != nil {
		t.Fatal(err)
	}
	c := r.Preflight(context.Background(), s)
	if !c.CanInstall() || c.Unpaired {
		t.Errorf("%+v", c)
	}
	s.Firmware, s.AccountUUID = "26.1.2", ""
	f.console.Close()
	c = r.Preflight(context.Background(), s)
	if c.FirmwareOK || c.ConsoleOK || !c.Unpaired || c.CanInstall() {
		t.Errorf("%+v", c)
	}
}

func TestUploadTimeout(t *testing.T) {
	f := newFakeSpeaker(t)
	f.noBoot = true
	r := newRunner(f, t.TempDir())
	r.cfg.Timing.UploadTimeout = 100 * time.Millisecond
	_, err := r.Install(context.Background(), "127.0.0.1", &recorder{})
	ue, ok := err.(*UserError)
	if !ok || ue.Key != "timeout_upload" {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallRestartsStalledSpeaker(t *testing.T) {
	f := newFakeSpeaker(t)
	f.stalls = 1
	r := newRunner(f, filepath.Join(t.TempDir(), "SoundTouch-Backup"))
	res, err := r.Install(context.Background(), "127.0.0.1", &recorder{})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !res.TarReceived || f.boots != 2 {
		t.Errorf("result %+v, boots %d (want 2)", res, f.boots)
	}
}

func TestInstallGivesUpAfterMaxRestarts(t *testing.T) {
	f := newFakeSpeaker(t)
	f.stalls = 10
	r := newRunner(f, filepath.Join(t.TempDir(), "SoundTouch-Backup"))
	r.cfg.Timing.UploadTimeout = 1500 * time.Millisecond
	_, err := r.Install(context.Background(), "127.0.0.1", &recorder{})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if f.boots != 3 {
		t.Errorf("boots %d, want 3 (1 + MaxRestarts)", f.boots)
	}
}
