#!/usr/bin/env python3
"""Local stand-in for a SoundTouch speaker, for testing the station page.

Serves speaker/ at http://127.0.0.1:8181/ and emulates the two CGI scripts:
  /cgi-bin/api?info|presets|now_playing|volume|sources   (GET)
  /cgi-bin/api?storePreset|select|key|removePreset       (POST, XML body)
  /cgi-bin/s?<base64url>                                 (decodes like the real one)
State is in memory only.

  python3 tools/mock_speaker.py            # normal
  python3 tools/mock_speaker.py --down     # speaker "restarting": api returns empty body
  python3 tools/mock_speaker.py --slow 15  # api answers after 15 s (tests timeouts)
"""
import argparse, base64, os, sys, threading, time
import xml.etree.ElementTree as ET
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from xml.sax.saxutils import escape, quoteattr

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "speaker")
ALLOWED = {"info", "presets", "now_playing", "volume", "sources", "storePreset",
           "removePreset", "select", "key"}

lock = threading.Lock()
presets = {
    "1": ('<ContentItem source="LOCAL_INTERNET_RADIO" type="stationurl" location="/1.json" '
          'sourceAccount="" isPresetable="true"><itemName>ANTENNE BAYERN Top 40</itemName>'
          '<containerArt>http://example.invalid/ab.png</containerArt></ContentItem>'),
    "2": ('<ContentItem source="TUNEIN" type="stationurl" location="/v1/playback/station/s24896" '
          'sourceAccount="" isPresetable="true"><itemName>SWR3</itemName></ContentItem>'),
    "4": ('<ContentItem source="SPOTIFY" type="tracklisturl" location="/playback/container/abc" '
          'sourceAccount="someone" isPresetable="true"><itemName>Discover Weekly</itemName></ContentItem>'),
}
state = {"item": presets["1"], "status": "PLAY_STATE", "power": True}
args = None


def presets_xml():
    out = "<presets>"
    for pid in sorted(presets):
        out += f'<preset id="{pid}" createdOn="1700000000" updatedOn="{int(time.time())}">{presets[pid]}</preset>'
    return out + "</presets>"


def now_playing_xml():
    if not state["power"]:
        return ('<nowPlaying deviceID="MOCK00000000" source="STANDBY">'
                '<ContentItem source="STANDBY" isPresetable="false" /></nowPlaying>')
    ci = ET.fromstring(state["item"])
    name = ci.findtext("itemName") or ""
    return (f'<nowPlaying deviceID="MOCK00000000" source={quoteattr(ci.get("source", ""))}>'
            f'{state["item"]}<track>Mock Track</track><artist>Mock Artist</artist>'
            f'<stationName>{escape(name)}</stationName><playStatus>{state["status"]}</playStatus>'
            f'</nowPlaying>')


def handle_api(path, body):
    if path == "info":
        return ('<info deviceID="MOCK00000000"><name>Küche (Mock)</name><type>SoundTouch 10</type>'
                '<margeAccountUUID>0</margeAccountUUID></info>')
    if path == "presets":
        return presets_xml()
    if path == "now_playing":
        return now_playing_xml()
    if path == "volume":
        return '<volume deviceID="MOCK00000000"><targetvolume>20</targetvolume><actualvolume>20</actualvolume><muteenabled>false</muteenabled></volume>'
    if path == "sources":
        return '<sources deviceID="MOCK00000000"><sourceItem source="LOCAL_INTERNET_RADIO" status="READY" /></sources>'
    if body is None:
        return '<errors><error value="1019" name="CLIENT_XML_ERROR">GET not allowed</error></errors>'
    try:
        el = ET.fromstring(body)
    except ET.ParseError as e:
        return f'<errors><error value="1019" name="CLIENT_XML_ERROR">{escape(str(e))}</error></errors>'
    if path == "storePreset":
        pid, ci = el.get("id"), el.find("ContentItem")
        if pid not in {"1", "2", "3", "4", "5", "6"} or ci is None:
            return '<errors><error value="1019" name="CLIENT_XML_ERROR">bad preset</error></errors>'
        presets[pid] = ET.tostring(ci, encoding="unicode")
        log(f"  stored preset {pid}: {ci.findtext('itemName')!r} -> {decode_loc(ci.get('location'))}")
        return presets_xml()
    if path == "removePreset":
        presets.pop(el.get("id"), None)
        return presets_xml()
    if path == "select":
        state.update(item=ET.tostring(el, encoding="unicode"), status="PLAY_STATE", power=True)
        log(f"  playing {el.findtext('itemName')!r} -> {decode_loc(el.get('location'))}")
        return "<status>/select</status>"
    if path == "key":
        k = (el.text or "").strip()
        if el.get("state") == "release":
            if k.startswith("PRESET_") and k[7:] in presets:
                state.update(item=presets[k[7:]], status="PLAY_STATE", power=True)
            elif k == "PAUSE":
                state["status"] = "PAUSE_STATE"
            elif k == "PLAY":
                state["status"] = "PLAY_STATE"
            elif k == "POWER":
                state["power"] = not state["power"]
            log(f"  key {k}")
        return "<status>/key</status>"
    return "<errors/>"


def decode_s(query):
    # same as speaker/cgi-bin/s: echo "$QUERY_STRING" | tr -- '-_' '+/' | base64 -d
    return base64.b64decode(query.translate(str.maketrans("-_", "+/")))


def decode_loc(loc):
    if loc and loc.startswith("/cgi-bin/s?"):
        try:
            return decode_s(loc[len("/cgi-bin/s?"):]).decode("utf-8")
        except Exception as e:  # noqa: BLE001
            return f"<undecodable: {e}>"
    return loc


def log(msg):
    sys.stderr.write(msg + "\n")


class H(SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=ROOT, **kw)

    def log_message(self, fmt, *a):
        log("%s %s" % (self.address_string(), fmt % a))

    def send_body(self, code, ctype, data):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def route(self, body):
        path, _, q = self.path.partition("?")
        if path == "/cgi-bin/s":
            try:
                data = decode_s(q)
            except Exception:  # noqa: BLE001
                data = b""
            return self.send_body(200, "application/json", data)
        if path == "/cgi-bin/api":
            if q not in ALLOWED:
                return self.send_body(400, "text/plain", b"not allowed\n")
            if body is not None:
                log("  body: " + body.decode("utf-8", "replace"))
            if args.slow:
                time.sleep(args.slow)
            if args.down:  # the real CGI prints headers, then curl fails -> empty 200
                return self.send_body(200, "text/xml", b"")
            with lock:
                out = handle_api(q, body.decode("utf-8") if body is not None else None)
            return self.send_body(200, "text/xml", out.encode("utf-8"))
        if body is not None:
            return self.send_body(405, "text/plain", b"")
        return super().do_GET()

    def do_GET(self):
        self.route(None)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        self.route(self.rfile.read(n))


def main():
    global args
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--port", type=int, default=8181)
    ap.add_argument("--down", action="store_true", help="api returns empty body (speaker restarting)")
    ap.add_argument("--slow", type=float, default=0, help="delay api answers by N seconds")
    args = ap.parse_args()
    srv = ThreadingHTTPServer(("127.0.0.1", args.port), H)
    log(f"Mock speaker on http://127.0.0.1:{args.port}/  (serving {os.path.normpath(ROOT)})")
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
