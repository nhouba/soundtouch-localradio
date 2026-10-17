package speaker

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"
)

// Console is a session on the speaker's plain-text console (TCP 17000).
type Console struct {
	conn  net.Conn
	delay time.Duration

	mu   sync.Mutex
	buf  strings.Builder
	done chan struct{}
}

// DialConsole opens a console session. delay is the pause after each command
// (the speaker needs about 2 s).
func DialConsole(ctx context.Context, addr string, delay time.Duration) (*Console, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &Console{conn: conn, delay: delay, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *Console) readLoop() {
	defer close(c.done)
	b := make([]byte, 4096)
	for {
		n, err := c.conn.Read(b)
		if n > 0 {
			c.mu.Lock()
			c.buf.Write(b[:n])
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Send writes cmd + CRLF, waits the command delay and returns everything the
// speaker has printed so far in this session.
func (c *Console) Send(ctx context.Context, cmd string) (string, error) {
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.conn.Write([]byte(cmd + "\r\n")); err != nil {
		return c.Output(), err
	}
	t := time.NewTimer(c.delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return c.Output(), ctx.Err()
	case <-t.C:
	}
	return c.Output(), nil
}

// Output returns all replies received so far, with CR removed.
func (c *Console) Output() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.ReplaceAll(c.buf.String(), "\r", "")
}

// Close ends the session.
func (c *Console) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

// ErrHookRejected means the speaker did not confirm "envswitch boseurls set".
var ErrHookRejected = errors.New("speaker did not confirm the new server URLs")

// RunCommands sends cmds in one console session. After an "envswitch" command
// the reply must contain "Setting Bose Server URLs"; otherwise no further
// command (in particular no reboot) is sent and ErrHookRejected is returned.
// Errors after "sys reboot" are ignored (the speaker drops the connection).
func RunCommands(ctx context.Context, addr string, delay time.Duration, cmds []string) (string, error) {
	c, err := DialConsole(ctx, addr, delay)
	if err != nil {
		return "", err
	}
	defer c.Close()
	confirmed := 0
	for _, cmd := range cmds {
		out, err := c.Send(ctx, cmd)
		if cmd == "sys reboot" {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(cmd, "envswitch ") {
			n := strings.Count(out, "Setting Bose Server URLs")
			if n <= confirmed {
				// slow speaker: give it a few more seconds
				select {
				case <-ctx.Done():
					return c.Output(), ctx.Err()
				case <-time.After(2 * delay):
				}
				out = c.Output()
				n = strings.Count(out, "Setting Bose Server URLs")
			}
			if n <= confirmed {
				return out, ErrHookRejected
			}
			confirmed = n
		}
	}
	return c.Output(), nil
}
