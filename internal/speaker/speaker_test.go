package speaker

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const info = `<?xml version="1.0" encoding="UTF-8" ?>
<info deviceID="689E19B8BB8A"><name>Wohnzimmer &amp; Küche</name><type>SoundTouch 10</type>
<margeAccountUUID>3230304</margeAccountUUID>
<components>
<component><componentCategory>LIGHTSWITCH</componentCategory><softwareVersion>0.0.1</softwareVersion></component>
<component><componentCategory>SCM</componentCategory><softwareVersion>27.0.3.4629.5041371 epdbuild</softwareVersion></component>
<component><componentCategory>PackagedProduct</componentCategory></component>
</components>
<margeURL>https://streaming.bose.com</margeURL></info>`

func TestParseInfo(t *testing.T) {
	s, err := ParseInfo("10.0.0.5", []byte(info))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Wohnzimmer & Küche" || s.Type != "SoundTouch 10" || s.DeviceID != "689E19B8BB8A" ||
		s.AccountUUID != "3230304" || s.Firmware != "27.0.3.4629.5041371 epdbuild" || s.IP != "10.0.0.5" {
		t.Errorf("%+v", s)
	}
	if s.FirmwareMajor() != 27 {
		t.Errorf("major %d", s.FirmwareMajor())
	}
	if _, err := ParseInfo("x", []byte("<html>router</html>")); err == nil {
		t.Error("accepted non-speaker document")
	}
	for v, want := range map[string]int{"": -1, "abc": -1, "26.1": 26, "100": 100} {
		if got := (Speaker{Firmware: v}).FirmwareMajor(); got != want {
			t.Errorf("%q: %d", v, got)
		}
	}
}

func TestHosts24(t *testing.T) {
	h := Hosts24([]net.IP{net.IPv4(192, 168, 178, 0)})
	if len(h) != 254 || h[0] != "192.168.178.1" || h[253] != "192.168.178.254" {
		t.Errorf("%d %v %v", len(h), h[0], h[len(h)-1])
	}
}

func TestScanLocalhostOnly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/info" {
			io.WriteString(w, info)
		}
	}))
	defer ts.Close()
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	port, _ := strconv.Atoi(p)
	c := &Client{Ports: Ports{API: port}, HTTP: &http.Client{Timeout: 500 * time.Millisecond}}
	found := c.Scan(context.Background(), []string{"127.0.0.1", "127.0.0.1"}, 4, nil)
	if len(found) != 2 || found[0].DeviceID != "689E19B8BB8A" {
		t.Errorf("%+v", found)
	}
}

// fakeConsole records the lines it receives.
func fakeConsole(t *testing.T, confirm bool) (string, func() []string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var got []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if !strings.HasSuffix(line, "\r\n") {
						t.Errorf("command not CRLF-terminated: %q", line)
					}
					line = strings.TrimSuffix(line, "\r\n")
					mu.Lock()
					got = append(got, line)
					mu.Unlock()
					switch {
					case line == "sys reboot":
						return
					case strings.HasPrefix(line, "envswitch") && confirm:
						io.WriteString(conn, "->Setting Bose Server URLs to x\r\nOK\r\n")
					default:
						io.WriteString(conn, "->OK\r\n")
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, got...) }
}

func TestRunCommandsSequence(t *testing.T) {
	addr, got := fakeConsole(t, true)
	cmds := []string{"sys configuration bmxRegistryUrl X", `envswitch boseurls set ";a ;" ;`, "sys reboot"}
	out, err := RunCommands(context.Background(), addr, 20*time.Millisecond, cmds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Setting Bose Server URLs") {
		t.Errorf("output %q", out)
	}
	time.Sleep(20 * time.Millisecond)
	if strings.Join(got(), "\n") != strings.Join(cmds, "\n") {
		t.Errorf("got %q", got())
	}
}

func TestRunCommandsStopsWhenNotConfirmed(t *testing.T) {
	addr, got := fakeConsole(t, false)
	cmds := []string{"sys configuration bmxRegistryUrl X", `envswitch boseurls set ";a ;" ;`, "sys reboot"}
	_, err := RunCommands(context.Background(), addr, 20*time.Millisecond, cmds)
	if !errors.Is(err, ErrHookRejected) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range got() {
		if c == "sys reboot" {
			t.Error("reboot sent after unconfirmed envswitch")
		}
	}
}
