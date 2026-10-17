// Package speaker talks to a Bose SoundTouch speaker: its web API (port 8090),
// its text console (port 17000) and the station page (port 8181).
package speaker

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Ports of the speaker services. Tests replace them with local mock ports.
type Ports struct {
	API     int // web API, 8090
	Console int // text console, 17000
	Page    int // station page served by the installed hook, 8181
}

// DefaultPorts are the real speaker ports.
var DefaultPorts = Ports{API: 8090, Console: 17000, Page: 8181}

// Speaker is one speaker as shown in the UI.
type Speaker struct {
	IP          string `json:"ip"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Firmware    string `json:"firmware"`
	DeviceID    string `json:"deviceID"`
	AccountUUID string `json:"accountUUID"`
}

// FirmwareMajor returns the leading number of the firmware version, or -1.
func (s Speaker) FirmwareMajor() int {
	v := s.Firmware
	if i := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		v = v[:i]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

type infoXML struct {
	XMLName     xml.Name `xml:"info"`
	DeviceID    string   `xml:"deviceID,attr"`
	Name        string   `xml:"name"`
	Type        string   `xml:"type"`
	AccountUUID string   `xml:"margeAccountUUID"`
	Components  []struct {
		Category string `xml:"componentCategory"`
		Version  string `xml:"softwareVersion"`
	} `xml:"components>component"`
}

// ParseInfo parses the XML of http://IP:8090/info.
func ParseInfo(ip string, data []byte) (Speaker, error) {
	var x infoXML
	if err := xml.Unmarshal(data, &x); err != nil {
		return Speaker{}, fmt.Errorf("not a SoundTouch info document: %w", err)
	}
	s := Speaker{
		IP:          ip,
		Name:        strings.TrimSpace(x.Name),
		Type:        strings.TrimSpace(x.Type),
		DeviceID:    strings.TrimSpace(x.DeviceID),
		AccountUUID: strings.TrimSpace(x.AccountUUID),
	}
	for _, c := range x.Components {
		v := strings.TrimSpace(c.Version)
		if v == "" {
			continue
		}
		if s.Firmware == "" || strings.EqualFold(c.Category, "SCM") {
			s.Firmware = v
		}
	}
	if s.DeviceID == "" && s.Name == "" {
		return Speaker{}, fmt.Errorf("info document has no deviceID or name")
	}
	return s, nil
}

// Client accesses one speaker's API.
type Client struct {
	Ports Ports
	HTTP  *http.Client
}

// NewClient returns a client for the real ports with the given timeout.
func NewClient(timeout time.Duration) *Client {
	return &Client{Ports: DefaultPorts, HTTP: &http.Client{Timeout: timeout}}
}

func (c *Client) apiURL(ip, path string) string {
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(c.Ports.API)) + "/" + path
}

// Get fetches http://ip:API/path (max 4 MiB).
func (c *Client) Get(ctx context.Context, ip, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(ip, path), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// Info fetches and parses /info.
func (c *Client) Info(ctx context.Context, ip string) (Speaker, error) {
	b, err := c.Get(ctx, ip, "info")
	if err != nil {
		return Speaker{}, err
	}
	return ParseInfo(ip, b)
}

// ConsoleReachable reports whether a TCP connection to the console port succeeds.
func (c *Client) ConsoleReachable(ctx context.Context, ip string, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(c.Ports.Console)))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ConsoleAddr is host:port of the console.
func (c *Client) ConsoleAddr(ip string) string {
	return net.JoinHostPort(ip, strconv.Itoa(c.Ports.Console))
}

// PageURL is the station page URL.
func (c *Client) PageURL(ip string) string {
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(c.Ports.Page)) + "/"
}

// PageUp reports whether the station page answers with HTTP 200.
func (c *Client) PageUp(ctx context.Context, ip string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.PageURL(ip), nil)
	if err != nil {
		return false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// LocalIPTowards returns this computer's IP address on the route to ip.
// No packet is sent (UDP "connect" only selects the route).
func LocalIPTowards(ip string) (string, error) {
	conn, err := net.Dial("udp4", net.JoinHostPort(ip, "9"))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
