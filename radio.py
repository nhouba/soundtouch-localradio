#!/usr/bin/env python3
"""soundtouch-localradio command line tool (the installer app does the same with a GUI).

  ./radio install                  one-time setup (speaker restarts, up to 5 min)
  ./radio search "swr3"            find stations (radio-browser.info)
  ./radio set 3 "swr3"             put the best match on button 3 (instant, no reboot)
  ./radio set 3 https://.../stream --name "My Station"
  ./radio list | status | play 3 | uninstall

After install, stations can also be changed on the speaker's station page:
http://<speaker-ip>:8181/  (phone or computer in the same network).
No dependencies besides Python 3.8+.
"""
import argparse
import base64
import http.server
import json
import os
import platform
import re
import socket
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path
from xml.sax.saxutils import escape

ROOT = Path(__file__).resolve().parent
SPEAKER_FILES = ROOT / "speaker"              # copied to /mnt/nv/localradio on the speaker
SERVED = ("registry.json", "index.html", "cgi-bin/s", "cgi-bin/api")
BACKUPS = ROOT / "backups"
SPEAKER_FILE = ROOT / ".speaker"              # remembers the speaker's IP (git-ignored)
REG = "http://127.0.0.1:8181/registry.json"   # registry served by the speaker itself
BUTTONS = range(1, 7)
ORIGINAL_URLS = "https://streaming.bose.com https://worldwide.bose.com/updates/soundtouch"
UA = "soundtouch-localradio/1.0"


def die(msg):
    sys.exit(f"error: {msg}")


# ---------------------------------------------------------------- speaker access

def discover():
    """Find SoundTouch speakers via mDNS. Returns [(name, ip)]."""
    found = []
    if platform.system() == "Darwin":
        names = _run_for("dns-sd", "-B", "_soundtouch._tcp", "local.", seconds=3)
        for line in names.splitlines():
            m = re.search(r"_soundtouch\._tcp\.\s+(.+)$", line)
            if m and " Add " in f" {line} ":
                name = m.group(1).strip()
                info = _run_for("dns-sd", "-L", name, "_soundtouch._tcp", "local.", seconds=2)
                h = re.search(r"can be reached at (\S+?)\.?:\d+", info)
                if h:
                    try:
                        found.append((name, socket.gethostbyname(h.group(1))))
                    except OSError:
                        pass
    else:
        out = _run_for("avahi-browse", "-rpt", "_soundtouch._tcp", seconds=5)
        for line in out.splitlines():
            f = line.split(";")
            if f[0] == "=" and len(f) > 7 and f[2] == "IPv4":
                found.append((f[3], f[7]))
    return sorted(set(found))


def _run_for(*cmd, seconds):
    try:
        p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    except FileNotFoundError:
        return ""
    try:
        out, _ = p.communicate(timeout=seconds)
    except subprocess.TimeoutExpired:
        p.terminate()
        out, _ = p.communicate()
    return out


def speaker_ip(args):
    ip = args.speaker or os.environ.get("SOUNDTOUCH")
    if not ip and SPEAKER_FILE.exists():
        ip = SPEAKER_FILE.read_text().strip()
    if not ip:
        print("Looking for SoundTouch speakers on the network ...")
        found = discover()
        if not found:
            die("no speaker found. Pass its IP: ./radio --speaker 192.168.x.y <command>")
        if len(found) > 1:
            for i, (n, a) in enumerate(found, 1):
                print(f"  {i}) {n}  {a}")
            ip = found[int(input("Which one? ")) - 1][1]
        else:
            ip = found[0][1]
            print(f"Found {found[0][0]} at {ip}")
    SPEAKER_FILE.write_text(ip + "\n")
    return ip


def api(ip, path, body=None, timeout=10):
    req = urllib.request.Request(f"http://{ip}:8090/{path}", data=body.encode() if body else None)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read().decode()


def console(ip, *cmds):
    """Send commands to the speaker's console (port 17000), return its replies."""
    out = b""
    with socket.create_connection((ip, 17000), timeout=10) as s:
        s.settimeout(2.5)
        for c in cmds:
            s.sendall(c.encode() + b"\r\n")
            time.sleep(2)
            try:
                out += s.recv(65536)
            except socket.timeout:
                pass
    return out.decode(errors="replace").replace("\r", "")


def local_ip_towards(ip):
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
        s.connect((ip, 9))
        return s.getsockname()[0]


# ---------------------------------------------------------------- stations

def station_location(name, url):
    """Preset location that carries the station itself: /cgi-bin/s?<base64url JSON>.
    speaker/cgi-bin/s decodes it, so no files need to change when stations change."""
    data = json.dumps({"audio": {"isRealtime": True, "hasPlaylist": False, "streamUrl": url},
                       "imageUrl": "", "name": name, "streamType": "liveRadio"},
                      separators=(",", ":"), ensure_ascii=False)
    b64 = base64.b64encode(data.encode()).decode().replace("+", "-").replace("/", "_")
    return "/cgi-bin/s?" + b64


def content_item(name, url):
    return (f'<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" '
            f'location="{station_location(name, url)}" sourceAccount="" isPresetable="true">'
            f"<itemName>{escape(name)}</itemName></ContentItem>")


def search(query, limit=10):
    q = urllib.parse.urlencode({"name": query, "limit": 40, "hidebroken": "true",
                                "order": "votes", "reverse": "true"})
    for host in ("de1", "fi1", "at1"):
        try:
            req = urllib.request.Request(f"https://{host}.api.radio-browser.info/json/stations/search?{q}",
                                         headers={"User-Agent": UA})
            with urllib.request.urlopen(req, timeout=10) as r:
                res = json.load(r)
            break
        except OSError:
            continue
    else:
        die("radio-browser.info not reachable")
    # the speaker can't play HLS or playlist files, only direct streams
    res = [s for s in res if s.get("url_resolved") and not s.get("hls")
           and not re.search(r"\.(m3u8?|pls|asx)(\?|$)", s["url_resolved"], re.I)]

    # prefer MP3/AAC (http and https both play; tested on firmware 27.0.3), then popularity
    def score(s):
        return (s["codec"] in ("MP3", "AAC", "AAC+"), s["votes"])
    return sorted(res, key=score, reverse=True)[:limit]


def check_stream(url):
    """Return (ok, info). Reads a few KB to make sure it is a live audio stream."""
    try:
        req = urllib.request.Request(url, headers={"User-Agent": UA, "Icy-MetaData": "0"})
        with urllib.request.urlopen(req, timeout=10) as r:
            ctype = r.headers.get("Content-Type", "")
            r.read(4096)
            if "mpegurl" in ctype or "scpls" in ctype or "html" in ctype:
                return False, f"not a direct stream ({ctype}); use the stream URL inside it"
            return True, ctype or "unknown type"
    except Exception as e:  # noqa: BLE001 - report any network failure
        return False, str(e)


# ---------------------------------------------------------------- boot hooks
# The speaker stores the hook in margeServerUrl and runs it as root (at boot, possibly
# periodically). It strips '&', so no '&&', '2>&1' or 'cmd &'; background via
# start-stop-daemon; a marker in /run (tmpfs) makes it start things once per boot.
# 'sys configuration' is only persisted when 'envswitch boseurls set' runs afterwards.
# Keep identical to the Go installer (internal/... hooks).

def hook_runtime():
    resend = f"(echo sys configuration bmxRegistryUrl {REG};sleep 2)|nc 127.0.0.1 17000"
    return ("if [ ! -e /run/localradio.started ];then touch /run/localradio.started;"
            "busybox httpd -p 8181 -h /mnt/nv/localradio;"
            f"start-stop-daemon -S -b -x /bin/sh -- -c 'sleep 25;{resend};sleep 60;{resend}';fi")


def hook_install(srv):
    backup = (f"(cd /mnt/nv;tar cf - BoseApp-Persistence OverrideSdkPrivateCfg.xml)"
              f"|curl -s -m30 -T - {srv}/up/speaker-nv.tar;")
    return ("mkdir -p /mnt/nv/localradio/cgi-bin;cd /mnt/nv/localradio;"
            f"if curl -sf -m5 -o /tmp/lr-registry.json {srv}/f/registry.json;then "
            f"for f in index.html cgi-bin/s cgi-bin/api;do curl -sf -m10 -o $f.new {srv}/f/$f;"
            "if [ -s $f.new ];then mv $f.new $f;fi;done;"
            "mv /tmp/lr-registry.json registry.json;chmod 755 cgi-bin/s cgi-bin/api;"
            f"{backup}ls -laR|curl -s -m5 -T - {srv}/up/done.txt;fi;" + hook_runtime())


def set_hook(ip, hook, reboot):
    cmds = [f"sys configuration bmxRegistryUrl {REG}", f'envswitch boseurls set ";{hook} ;" ;']
    if reboot:
        cmds.append("sys reboot")
    out = console(ip, *cmds)
    if "Setting Bose Server URLs" not in out:
        die(f"speaker did not accept the hook:\n{out}")


# ---------------------------------------------------------------- temporary file server

def start_server(backup_dir):
    done = threading.Event()

    class H(http.server.BaseHTTPRequestHandler):
        def log_message(self, *a):
            pass

        def do_GET(self):
            rel = urllib.parse.urlparse(self.path).path[len("/f/"):]
            ok = self.path.startswith("/f/") and rel in SERVED and (SPEAKER_FILES / rel).is_file()
            body = (SPEAKER_FILES / rel).read_bytes() if ok else b""
            self.send_response(200 if ok else 404)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_PUT(self):
            data = b""
            if "chunked" in self.headers.get("Transfer-Encoding", ""):  # speaker's curl -T -
                while (n := int(self.rfile.readline().split(b";")[0].strip(), 16)):
                    data += self.rfile.read(n)
                    self.rfile.readline()
                self.rfile.readline()
            else:
                data = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            name = os.path.basename(self.path)
            if name in ("speaker-nv.tar", "done.txt"):
                (backup_dir / name).write_bytes(data)
                os.chmod(backup_dir / name, 0o600)  # may contain device keys / Wi-Fi password
            self.send_response(200)
            self.send_header("Content-Length", "0")
            self.end_headers()
            if name == "done.txt":
                done.set()

    srv = http.server.ThreadingHTTPServer(("0.0.0.0", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, done


# ---------------------------------------------------------------- commands

def presets(ip):
    """{button: (name, source, location)} as stored on the speaker."""
    out = {}
    for p in ET.fromstring(api(ip, "presets")):
        c = p.find("ContentItem")
        out[int(p.get("id"))] = (c.findtext("itemName") or "", c.get("source"), c.get("location") or "")
    return out


def cmd_install(args):
    ip = speaker_ip(args)
    info = api(ip, "info")
    fw = re.search(r"<softwareVersion>(\d+)", info)
    if not fw or int(fw.group(1)) < 27:
        die("firmware 27.x or newer required")
    if not re.search(r"<margeAccountUUID>\w+", info):
        print("warning: speaker looks factory-reset/unpaired (no margeAccountUUID); untested")
    try:
        socket.create_connection((ip, 17000), timeout=3).close()
    except OSError:
        die("speaker console (port 17000) not reachable")
    bdir = BACKUPS / time.strftime("%Y%m%d-%H%M%S")
    bdir.mkdir(parents=True, exist_ok=True)
    os.chmod(bdir, 0o700)
    for e in ("info", "presets", "sources"):
        (bdir / f"{e}.xml").write_text(api(ip, e))
    srv, done = start_server(bdir)
    url = f"http://{local_ip_towards(ip)}:{srv.server_address[1]}"
    print(f"Installing on {ip}. The speaker restarts (up to 5 minutes) ...")
    set_hook(ip, hook_install(url), reboot=True)
    if not done.wait(360):
        srv.shutdown()
        die("speaker did not fetch the files within 6 min (firewall? see README troubleshooting)")
    srv.shutdown()
    set_hook(ip, hook_runtime(), reboot=False)  # drop the download part again
    print(f"Backup saved in {bdir} (contains device keys + Wi-Fi password, keep private)")
    print("Waiting for the speaker to load the station list ...", flush=True)
    time.sleep(40)
    print(f"Done. Station page: http://{ip}:8181/   or: ./radio set 1 \"station name\"")


def cmd_set(args):
    ip = speaker_ip(args)
    if args.button not in BUTTONS:
        die("button must be 1-6")
    q = " ".join(args.station)
    if re.match(r"https?://", q):
        url, name = q, args.name or urllib.parse.urlparse(q).hostname
    else:
        res = search(q)
        if not res:
            die(f'nothing found for "{q}". Try ./radio search "{q}" with fewer words')
        s = res[args.pick - 1] if args.pick else res[0]
        url, name = s["url_resolved"], args.name or s["name"].strip()
        print(f"Found: {name}  ({s['codec']} {s['bitrate'] or '?'} kbps, {s['country'] or '?'})")
    ok, info = check_stream(url)
    if not ok and not args.force:
        hint = ("Check the URL, or add --force to use it anyway" if re.match(r"https?://", q)
                else f'Try ./radio search "{q}" and --pick another one')
        die(f"stream doesn't play: {info}\n  {url}\n{hint}")
    print(f"Stream {'OK' if ok else 'NOT verified'} ({info}): {url}")
    api(ip, "storePreset", f'<preset id="{args.button}">{content_item(name, url)}</preset>')
    print(f"Button {args.button} = {name}")


def cmd_search(args):
    for i, s in enumerate(search(" ".join(args.query)), 1):
        print(f"{i:2}) {s['name'].strip()[:40]:40} {s['codec']:5} {str(s['bitrate'] or '?'):>4} kbps"
              f"  {(s['countrycode'] or '--')}  {s['url_resolved']}")
    print('\nUse one: ./radio set <button> "<name>" --pick <number>')


def cmd_list(args):
    ip = speaker_ip(args)
    p = presets(ip)
    for n in BUTTONS:
        name, source, loc = p.get(n, ("(empty)", "", ""))
        works = source == "LOCAL_INTERNET_RADIO" or source not in ("TUNEIN", "")
        print(f"  {n}  {name:35} {source}{'' if works else '  (no longer works)'}")


def cmd_status(args):
    ip = speaker_ip(args)
    info = ET.fromstring(api(ip, "info"))
    print(f"{info.findtext('name')} ({info.findtext('type')}) at {ip}, firmware "
          f"{info.findtext('.//softwareVersion').split()[0]}")
    np = ET.fromstring(api(ip, "now_playing"))
    print(f"Now: {np.get('source')}  {np.findtext('stationName') or np.findtext('track') or ''}  "
          f"{np.findtext('playStatus') or ''}")
    try:
        urllib.request.urlopen(f"http://{ip}:8181/registry.json", timeout=3).read()
        print(f"Station page: http://{ip}:8181/  (running)")
    except OSError:
        print("Station server on the speaker: not running (not installed, or just rebooted)")
    cmd_list(args)


def cmd_play(args):
    ip = speaker_ip(args)
    for state in ("press", "release"):
        api(ip, "key", f'<key state="{state}" sender="Gabbo">PRESET_{args.button}</key>')
    time.sleep(8)
    np = ET.fromstring(api(ip, "now_playing"))
    print(f"{np.get('source')}  {np.findtext('stationName') or ''}  {np.findtext('playStatus') or ''}")
    if np.get("source") == "INVALID_SOURCE":
        print("Not ready yet? Right after a reboot it takes ~1-2 min. Otherwise run ./radio install")


def cmd_uninstall(args):
    ip = speaker_ip(args)
    console(ip, f"envswitch boseurls set {ORIGINAL_URLS}", "sys reboot")
    print("Boot hook removed, speaker reboots. Radio presets no longer work.")


def main():
    p = argparse.ArgumentParser(prog="radio", description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--speaker", help="speaker IP (default: remembered / auto-discovered)")
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("set", help="put a station on a button (search by name or give a URL)")
    s.add_argument("button", type=int)
    s.add_argument("station", nargs="+", help="station name to search, or a stream URL")
    s.add_argument("--name", help="display name (default: from search result)")
    s.add_argument("--pick", type=int, help="use result number N from ./radio search")
    s.add_argument("--force", action="store_true", help="skip the stream check")
    s.set_defaults(func=cmd_set)

    s = sub.add_parser("search", help="search radio-browser.info")
    s.add_argument("query", nargs="+")
    s.set_defaults(func=cmd_search)

    sub.add_parser("list", help="show the speaker's preset buttons").set_defaults(func=cmd_list)
    sub.add_parser("status", help="speaker, now playing, station server, presets").set_defaults(func=cmd_status)
    sub.add_parser("install", help="one-time setup (1 reboot)").set_defaults(func=cmd_install)
    sub.add_parser("uninstall", help="remove the boot hook").set_defaults(func=cmd_uninstall)
    s = sub.add_parser("play", help="press a preset button remotely")
    s.add_argument("button", type=int)
    s.set_defaults(func=cmd_play)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
