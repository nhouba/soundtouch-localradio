// Package i18n holds every user-facing text of the installer, so that other
// languages (e.g. German) can be added by adding one more map.
//
// Texts may contain %s placeholders, filled in order.
package i18n

import (
	"fmt"
	"strings"
)

// Default is the fallback language.
const Default = "en"

var messages = map[string]map[string]string{
	"en": {
		"app.title":   "SoundTouch Radio Installer",
		"app.intro":   "This puts a small radio station list onto your Bose SoundTouch speaker, so its preset buttons play internet radio again. Your computer is only needed during setup.",
		"app.lost":    "Lost connection to the installer. Is the installer window still open? Start the installer again and reload this page.",
		"app.working": "Working…",

		"find.title":         "Choose your speaker",
		"find.searching":     "Looking for speakers in your network…",
		"find.none":          "No speaker found. Make sure the speaker is switched on and connected to the same Wi-Fi as this computer, then search again, or type its IP address below.",
		"find.again":         "Search again",
		"find.manual.label":  "Speaker IP address",
		"find.manual.hint":   "You find it in the SoundTouch app under Settings, Speaker Settings, About, or in your router's list of devices. Example: 192.168.178.23",
		"find.manual.button": "Add speaker",
		"find.checking":      "Checking the speaker…",

		"spk.firmware":  "Firmware %s",
		"spk.ip":        "IP address %s",
		"spk.install":   "Install",
		"spk.uninstall": "Uninstall",
		"spk.ready":     "Ready to install.",

		"pre.firmware": "This speaker has firmware %s. Version 27 or newer is required. Update the speaker first (SoundTouch app, Settings, Speaker Settings), then search again.",
		"pre.console":  "The installer cannot reach the speaker's service port (17000). Make sure this computer is in the same network as the speaker (not a guest Wi-Fi), unplug the speaker for 10 seconds, wait 2 minutes and search again.",
		"pre.unpaired": "This speaker looks factory-reset or not set up (it has no account). This is untested and may not work.",

		"inst.title":    "Installing on %s",
		"inst.firewall": "If your computer asks whether this program may accept incoming network connections, click Allow. The speaker needs to download a few files from this computer.",
		"inst.keepopen": "Keep this page and the installer open until it is finished. Do not unplug the speaker. This takes up to 8 minutes.",
		"un.title":      "Uninstalling from %s",

		"step.backup_api": "Connecting to the speaker and saving its settings (waits if it is still restarting)",
		"step.fileserver": "Preparing the files for the speaker",
		"step.hook":       "Sending the setup to the speaker",
		"step.restart":    "Speaker is restarting (up to 5 minutes, it may restart twice)…",
		"step.finalize":   "Finishing the setup",
		"step.wait":       "Starting the station page (about 1 minute)…",
		"step.un_send":    "Restoring the original Bose settings",

		"state.pending": "waiting",
		"state.active":  "in progress",
		"state.done":    "done",
		"state.failed":  "failed",

		"done.title":    "Done! Your speaker has its own radio stations now.",
		"done.open":     "Open station page",
		"done.phone":    "The station page also works on your phone or tablet: open %s in its browser while it is connected to the same Wi-Fi. You can bookmark it.",
		"done.backup":   "A backup of the speaker was saved in this folder: %s",
		"done.private":  "It contains the speaker's private key and your Wi-Fi password. Keep it private and do not share it.",
		"done.notyet":   "The station page did not answer yet. Wait two minutes and then open it. If it still does not work, unplug the speaker for 10 seconds, wait 2 minutes and try again.",
		"done.notar":    "The speaker did not send its full settings backup. The radio still works; only that extra backup is missing.",
		"done.close":    "You can close this page and the installer window now.",
		"done.quit":     "Close installer",
		"done.quitted":  "The installer has been closed. You can close this page.",
		"un.done.title": "Uninstalled",
		"un.done.text":  "The speaker is now restarting with the original Bose settings. This takes about 2 minutes: please wait before you install again or use the speaker. The preset buttons no longer play the radio stations. Your backup files stay on this computer.",

		"un.confirm.title": "Remove the radio setup from %s?",
		"un.confirm.text":  "The speaker restarts and goes back to the original Bose settings. The preset buttons will stop playing radio. Your backup files stay on this computer.",
		"un.confirm.yes":   "Yes, uninstall",
		"un.confirm.no":    "Cancel",

		"common.back":  "Back to start",
		"common.retry": "Try again",
		"err.title":    "Something went wrong",
		"err.next":     "What to do:",
		"err.details":  "Technical details",

		"err.badip.text":          "“%s” is not a valid IP address.",
		"err.badip.next":          "Type four numbers separated by dots, for example 192.168.178.23.",
		"err.info.text":           "No SoundTouch speaker answered at %s.",
		"err.info.next":           "Check the IP address, make sure the speaker is switched on and in the same network as this computer, and try again.",
		"err.busy.text":           "The installer is already working on a speaker.",
		"err.busy.next":           "Wait until it has finished.",
		"err.blocked.text":        "This speaker cannot be set up right now.",
		"err.blocked.next":        "See the notes shown next to the speaker.",
		"err.backup_dir.text":     "Could not create the backup folder %s.",
		"err.backup_dir.next":     "Make sure your home folder is writable and has some free space, then try again.",
		"err.backup_api.text":     "Could not read the speaker's current settings. Nothing was changed.",
		"err.backup_api.next":     "Check that the speaker is still switched on and try again.",
		"err.localip.text":        "Could not find this computer's network address towards the speaker. Nothing was changed.",
		"err.localip.next":        "Connect this computer to the same Wi-Fi as the speaker and try again.",
		"err.fileserver.text":     "Could not start the temporary file server on this computer. Nothing was changed.",
		"err.fileserver.next":     "Close the installer, start it again and try again.",
		"err.console.text":        "Could not talk to the speaker's service port (17000). Nothing was changed.",
		"err.console.next":        "Unplug the speaker for 10 seconds, plug it back in, wait 2 minutes and try again.",
		"err.rejected.text":       "The speaker did not accept the setup. Nothing was changed.",
		"err.rejected.next":       "Unplug the speaker for 10 seconds, plug it back in, wait 2 minutes and try again.",
		"err.timeout_upload.text": "The speaker restarted but did not download the files within 6 minutes.",
		"err.timeout_upload.next": "Most likely a firewall on this computer blocked the speaker. Allow the installer to accept incoming connections (on Windows: allow access for private networks), make sure the computer does not go to sleep, and click Try again. The speaker keeps working normally in the meantime.",
		"err.finalize.text":       "The files are on the speaker, but the last step could not be completed because the speaker did not answer.",
		"err.finalize.next":       "Wait two minutes and click Try again. Running the setup again is safe.",
		"err.uninstall.text":      "Could not send the uninstall command to the speaker.",
		"err.uninstall.next":      "Unplug the speaker for 10 seconds, plug it back in, wait 2 minutes and try again.",
		"err.internal.text":       "An unexpected error happened.",
		"err.internal.next":       "Close the installer, start it again and try again.",
		"err.cancelled.text":      "The installation was stopped.",
		"err.cancelled.next":      "Click Try again to start it again.",
		"cli.started":             "SoundTouch Radio Installer is running.",
		"cli.open":                "Open this address in your web browser if it did not open by itself:",
		"cli.keep":                "Keep this window open until the installation is finished. Press Ctrl+C to quit.",
	},
}

// Languages lists the available languages.
func Languages() []string {
	out := make([]string, 0, len(messages))
	for l := range messages {
		out = append(out, l)
	}
	return out
}

// Pick chooses the best language for an Accept-Language header or a locale
// such as "de_DE.UTF-8".
func Pick(pref string) string {
	for _, part := range strings.Split(pref, ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if i := strings.IndexAny(tag, "-_."); i >= 0 {
			tag = tag[:i]
		}
		tag = strings.ToLower(tag)
		if _, ok := messages[tag]; ok {
			return tag
		}
	}
	return Default
}

// T returns the text for key in lang (falling back to English, then to the
// key itself) with %s placeholders filled from args.
func T(lang, key string, args ...any) string {
	s, ok := messages[lang][key]
	if !ok {
		s, ok = messages[Default][key]
	}
	if !ok {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// All returns all texts of lang (with English fallbacks), unformatted.
func All(lang string) map[string]string {
	out := map[string]string{}
	for k, v := range messages[Default] {
		out[k] = v
	}
	for k, v := range messages[lang] {
		out[k] = v
	}
	return out
}
