// Package hook builds the shell snippets that the speaker stores as its
// margeServerUrl and runs as root at boot.
//
// The speaker strips '&' from the value, and the value is passed to the console
// inside double quotes, so the snippets must contain neither '&' nor '"'.
// Keep these byte-for-byte identical to radio.py (hook_runtime / hook_install).
package hook

import (
	"fmt"
	"strings"
)

// Registry is the BMX registry served by the speaker itself.
const Registry = "http://127.0.0.1:8181/registry.json"

// OriginalURLs are the arguments of "envswitch boseurls set" that restore the
// factory Bose server URLs (removes any hook).
const OriginalURLs = "https://streaming.bose.com https://worldwide.bose.com/updates/soundtouch"

// Runtime starts busybox httpd on port 8181 once per boot and re-sends the
// registry URL to the speaker's console after the app has started.
func Runtime() string {
	resend := "(echo sys configuration bmxRegistryUrl " + Registry + ";sleep 2)|nc 127.0.0.1 17000"
	return "if [ ! -e /run/localradio.started ];then touch /run/localradio.started;" +
		"busybox httpd -p 8181 -h /mnt/nv/localradio;" +
		"start-stop-daemon -S -b -x /bin/sh -- -c 'sleep 25;" + resend + ";sleep 60;" + resend + "';fi"
}

// Backup uploads the speaker's persistent configuration to srv. Ends with ';'.
func Backup(srv string) string {
	return "(cd /mnt/nv;tar cf - BoseApp-Persistence OverrideSdkPrivateCfg.xml)" +
		"|curl -s -m30 -T - " + srv + "/up/speaker-nv.tar;"
}

// Setup downloads and runs the speaker/setup script from srv (if served). The
// script makes LOCAL_INTERNET_RADIO registrable on a speaker that was never
// paired to a Bose account; it is a no-op on paired speakers. Ends with ';'.
// It carries no '&' or '"', so it is safe inside the stored hook.
func Setup(srv string) string {
	return "curl -sf -m10 -o /tmp/lr-setup " + srv + "/f/setup;" +
		"if [ -s /tmp/lr-setup ];then sh /tmp/lr-setup;fi;"
}

// Install downloads the station files from srv (if reachable), runs the setup
// script, uploads a backup and a directory listing (done.txt), then runs Runtime.
func Install(srv string) string {
	return "mkdir -p /mnt/nv/localradio/cgi-bin;cd /mnt/nv/localradio;" +
		"if curl -sf -m5 -o /tmp/lr-registry.json " + srv + "/f/registry.json;then " +
		"for f in index.html cgi-bin/s cgi-bin/api;do curl -sf -m10 -o $f.new " + srv + "/f/$f;" +
		"if [ -s $f.new ];then mv $f.new $f;fi;done;" +
		"mv /tmp/lr-registry.json registry.json;chmod 755 cgi-bin/s cgi-bin/api;" +
		Setup(srv) +
		Backup(srv) + "ls -laR|curl -s -m5 -T - " + srv + "/up/done.txt;fi;" + Runtime()
}

// Validate reports an error if h cannot be stored safely by the speaker.
func Validate(h string) error {
	if i := strings.IndexAny(h, "&\"\r\n"); i >= 0 {
		return fmt.Errorf("hook contains forbidden character %q at %d", h[i], i)
	}
	return nil
}

// SetCommand is the console command that stores hook h as the boot hook.
func SetCommand(h string) string {
	return `envswitch boseurls set ";` + h + ` ;" ;`
}

// RegistryCommand sets the BMX registry URL (only persisted when followed by
// SetCommand in the same console session).
func RegistryCommand() string {
	return "sys configuration bmxRegistryUrl " + Registry
}

// AccountCommand stores a non-empty margeAccountUUID on the speaker. Speakers
// set up after the Bose cloud shutdown have none, and the boot hook only runs
// once the speaker is "paired", so the installer assigns a local id first.
func AccountCommand(accountID string) string {
	return "envswitch accountid set " + accountID
}

// InstallCommands is the console sequence that installs and reboots. When
// accountID is non-empty (an unpaired speaker), it is assigned first so the
// boot hook will run.
func InstallCommands(srv, accountID string) []string {
	var cmds []string
	if accountID != "" {
		cmds = append(cmds, AccountCommand(accountID))
	}
	return append(cmds, RegistryCommand(), SetCommand(Install(srv)), "sys reboot")
}

// FinalizeCommands replaces the install hook by the runtime-only hook (no reboot).
func FinalizeCommands() []string {
	return []string{RegistryCommand(), SetCommand(Runtime())}
}

// UninstallCommands restores the original Bose URLs and reboots.
func UninstallCommands() []string {
	return []string{"envswitch boseurls set " + OriginalURLs, "sys reboot"}
}
