package hook

import (
	"strings"
	"testing"
)

const srv = "http://192.168.1.5:40123"

// Written out by hand from the specification (not built from the same pieces).
const wantRuntime = `if [ ! -e /run/localradio.started ];then touch /run/localradio.started;busybox httpd -p 8181 -h /mnt/nv/localradio;start-stop-daemon -S -b -x /bin/sh -- -c 'sleep 25;(echo sys configuration bmxRegistryUrl http://127.0.0.1:8181/registry.json;sleep 2)|nc 127.0.0.1 17000;sleep 60;(echo sys configuration bmxRegistryUrl http://127.0.0.1:8181/registry.json;sleep 2)|nc 127.0.0.1 17000';fi`

const wantBackup = `(cd /mnt/nv;tar cf - BoseApp-Persistence OverrideSdkPrivateCfg.xml)|curl -s -m30 -T - http://192.168.1.5:40123/up/speaker-nv.tar;`

const wantInstall = `mkdir -p /mnt/nv/localradio/cgi-bin;cd /mnt/nv/localradio;if curl -sf -m5 -o /tmp/lr-registry.json http://192.168.1.5:40123/f/registry.json;then for f in index.html cgi-bin/s cgi-bin/api;do curl -sf -m10 -o $f.new http://192.168.1.5:40123/f/$f;if [ -s $f.new ];then mv $f.new $f;fi;done;mv /tmp/lr-registry.json registry.json;chmod 755 cgi-bin/s cgi-bin/api;` +
	wantBackup + `ls -laR|curl -s -m5 -T - http://192.168.1.5:40123/up/done.txt;fi;` + wantRuntime

func TestExactStrings(t *testing.T) {
	if got := Runtime(); got != wantRuntime {
		t.Errorf("Runtime mismatch:\n got %s\nwant %s", got, wantRuntime)
	}
	if got := Backup(srv); got != wantBackup {
		t.Errorf("Backup mismatch:\n got %s\nwant %s", got, wantBackup)
	}
	if got := Install(srv); got != wantInstall {
		t.Errorf("Install mismatch:\n got %s\nwant %s", got, wantInstall)
	}
}

func TestNoForbiddenChars(t *testing.T) {
	for name, h := range map[string]string{"runtime": Runtime(), "install": Install(srv), "backup": Backup(srv)} {
		if strings.ContainsAny(h, "&\"") {
			t.Errorf("%s hook contains '&' or '\"': %s", name, h)
		}
		if err := Validate(h); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if Validate("a&b") == nil || Validate(`a"b`) == nil {
		t.Error("Validate accepted forbidden characters")
	}
}

func TestCommands(t *testing.T) {
	inst := InstallCommands(srv)
	want := []string{
		"sys configuration bmxRegistryUrl http://127.0.0.1:8181/registry.json",
		`envswitch boseurls set ";` + wantInstall + ` ;" ;`,
		"sys reboot",
	}
	if strings.Join(inst, "\n") != strings.Join(want, "\n") {
		t.Errorf("InstallCommands:\n%q\nwant\n%q", inst, want)
	}
	fin := FinalizeCommands()
	if len(fin) != 2 || fin[0] != want[0] || fin[1] != `envswitch boseurls set ";`+wantRuntime+` ;" ;` {
		t.Errorf("FinalizeCommands: %q", fin)
	}
	un := UninstallCommands()
	if len(un) != 2 || un[0] != "envswitch boseurls set https://streaming.bose.com https://worldwide.bose.com/updates/soundtouch" || un[1] != "sys reboot" {
		t.Errorf("UninstallCommands: %q", un)
	}
}
