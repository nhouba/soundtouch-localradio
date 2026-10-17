package fileserver

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"registry.json": {Data: []byte(`{"reg":1}`)},
		"index.html":    {Data: []byte("<h1>x</h1>")},
		"cgi-bin/s":     {Data: []byte("#!/bin/sh s")},
		"cgi-bin/api":   {Data: []byte("#!/bin/sh api")},
		"secret.txt":    {Data: []byte("must not be served")},
	}
}

func start(t *testing.T) (*Server, string, string) {
	t.Helper()
	dir := t.TempDir()
	s := New(testFS(), dir, nil)
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, fmt.Sprintf("127.0.0.1:%d", s.Port()), dir
}

// rawRequest sends a request exactly as written (no client-side path cleaning).
func rawRequest(t *testing.T, addr, req string) (int, string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	for err == nil && resp.StatusCode < 200 { // skip "100 Continue"
		resp, err = http.ReadResponse(br, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestServesOnlyTheFourFiles(t *testing.T) {
	_, addr, _ := start(t)
	for _, f := range Served {
		code, body := rawRequest(t, addr, "GET /f/"+f+" HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		want, _ := testFS().ReadFile(f)
		if code != 200 || body != string(want) {
			t.Errorf("%s: %d %q", f, code, body)
		}
	}
	bad := []string{
		"/f/secret.txt", "/f/../secret.txt", "/f/cgi-bin/../secret.txt", "/f/%2e%2e/secret.txt",
		"/f/cgi-bin/../registry.json", "/f/./registry.json", "/f//registry.json", "/f/", "/f/cgi-bin",
		"/f/cgi-bin/", "/registry.json", "/f/registry.json/", "/up/done.txt", "/f/..%2fsecret.txt",
		"/f/REGISTRY.JSON", "/../../etc/passwd",
	}
	for _, p := range bad {
		code, body := rawRequest(t, addr, "GET "+p+" HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		if code == 200 {
			t.Errorf("%s served: %q", p, body)
		}
	}
}

func TestChunkedPut(t *testing.T) {
	s, addr, dir := start(t)
	chunks := []string{"hello ", "chunked ", "world"}
	var req strings.Builder
	req.WriteString("PUT /up/speaker-nv.tar HTTP/1.1\r\nHost: x\r\nUser-Agent: curl/7.x\r\nAccept: */*\r\nTransfer-Encoding: chunked\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n")
	for _, c := range chunks {
		fmt.Fprintf(&req, "%x\r\n%s\r\n", len(c), c)
	}
	req.WriteString("0\r\n\r\n")
	code, _ := rawRequest(t, addr, req.String())
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	got, err := os.ReadFile(filepath.Join(dir, "speaker-nv.tar"))
	if err != nil || string(got) != "hello chunked world" {
		t.Fatalf("saved %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "speaker-nv.tar"))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v, want 0600", st.Mode().Perm())
		}
	}
	if s.Received("speaker-nv.tar") != int64(len("hello chunked world")) {
		t.Error("Received size wrong")
	}
	select {
	case <-s.Done():
		t.Fatal("done before done.txt")
	default:
	}

	// done.txt with Content-Length closes Done.
	code, _ = rawRequest(t, addr, "PUT /up/done.txt HTTP/1.1\r\nHost: x\r\nContent-Length: 3\r\nConnection: close\r\n\r\nabc")
	if code != 200 {
		t.Fatalf("done.txt status %d", code)
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed")
	}
}

func TestRejectsOtherUploads(t *testing.T) {
	_, addr, dir := start(t)
	for _, p := range []string{"/up/evil.sh", "/up/../done.txt", "/up/sub/done.txt", "/up/", "/f/registry.json"} {
		code, _ := rawRequest(t, addr, "PUT "+p+" HTTP/1.1\r\nHost: x\r\nContent-Length: 1\r\nConnection: close\r\n\r\nx")
		if code == 200 {
			t.Errorf("PUT %s accepted", p)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("files written: %v", entries)
	}
}
