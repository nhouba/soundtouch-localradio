// Package flow runs the install and uninstall procedures step by step.
package flow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/nhouba/soundtouch-localradio/internal/fileserver"
	"github.com/nhouba/soundtouch-localradio/internal/hook"
	"github.com/nhouba/soundtouch-localradio/internal/speaker"
)

// UserError is an error with a translatable explanation (i18n keys
// "err.<Key>.text" and "err.<Key>.next").
type UserError struct {
	Key    string
	Args   []any
	Detail error
}

func (e *UserError) Error() string {
	if e.Detail != nil {
		return e.Key + ": " + e.Detail.Error()
	}
	return e.Key
}

func (e *UserError) Unwrap() error { return e.Detail }

func uerr(key string, detail error, args ...any) *UserError {
	return &UserError{Key: key, Args: args, Detail: detail}
}

// Step ids, in order (i18n key "step.<id>").
var (
	InstallSteps   = []string{"backup_api", "fileserver", "hook", "restart", "finalize", "wait"}
	UninstallSteps = []string{"un_send"}
)

// Step states.
const (
	Pending = "pending"
	Active  = "active"
	Done    = "done"
	Failed  = "failed"
)

// Progress receives step updates and log lines.
type Progress interface {
	Step(id, state string)
	Log(msg string)
}

// Timing holds the delays and timeouts of the procedure.
type Timing struct {
	CmdDelay      time.Duration // pause after each console command
	UploadTimeout time.Duration // speaker must upload done.txt within this
	ConsoleWait   time.Duration // how long to retry the console after the reboot
	ConsoleRetry  time.Duration // pause between console attempts
	FinalWait     time.Duration // wait after the final hook before checking the page
	PageTimeout   time.Duration // how long to poll the station page
	StallTimeout  time.Duration // restart the speaker if it stops partway through the downloads
	MaxRestarts   int           // how often to do that
}

// DefaultTiming matches the behaviour of the real speaker.
var DefaultTiming = Timing{
	CmdDelay:      2 * time.Second,
	UploadTimeout: 6 * time.Minute,
	ConsoleWait:   3 * time.Minute,
	ConsoleRetry:  5 * time.Second,
	FinalWait:     40 * time.Second,
	PageTimeout:   90 * time.Second,
	StallTimeout:  60 * time.Second,
	MaxRestarts:   2,
}

// Config configures a Runner.
type Config struct {
	Client     *speaker.Client
	Files      fs.FS  // speaker/ directory
	BackupRoot string // e.g. ~/SoundTouch-Backup
	ListenAddr string // file server address, "0.0.0.0:0" if empty
	Timing     Timing
	Now        func() time.Time
}

// Runner executes procedures.
type Runner struct{ cfg Config }

// New returns a Runner; zero Timing/Now/ListenAddr get defaults.
func New(cfg Config) *Runner {
	if cfg.Timing == (Timing{}) {
		cfg.Timing = DefaultTiming
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "0.0.0.0:0"
	}
	return &Runner{cfg: cfg}
}

// Checks is the preflight result for one speaker.
type Checks struct {
	FirmwareOK bool `json:"firmwareOK"`
	ConsoleOK  bool `json:"consoleOK"`
	Unpaired   bool `json:"unpaired"`
}

// CanInstall reports whether installation is allowed.
func (c Checks) CanInstall() bool { return c.FirmwareOK && c.ConsoleOK }

// Preflight checks firmware, console port and pairing.
func (r *Runner) Preflight(ctx context.Context, s speaker.Speaker) Checks {
	return Checks{
		FirmwareOK: s.FirmwareMajor() >= 27,
		ConsoleOK:  r.cfg.Client.ConsoleReachable(ctx, s.IP, 3*time.Second),
		Unpaired:   s.AccountUUID == "",
	}
}

// Result of a successful installation.
type Result struct {
	PageURL     string `json:"pageURL"`
	BackupDir   string `json:"backupDir"`
	PageUp      bool   `json:"pageUp"`
	TarReceived bool   `json:"tarReceived"`
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// BackupDirName returns <deviceID>-<YYYYMMDD-HHMMSS>.
func BackupDirName(s speaker.Speaker, t time.Time) string {
	id := unsafeChars.ReplaceAllString(s.DeviceID, "")
	if id == "" {
		id = unsafeChars.ReplaceAllString(s.IP, "-")
	}
	return id + "-" + t.Format("20060102-150405")
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// run marks step id active, runs f and marks it done or failed.
func run(p Progress, id string, f func() error) error {
	p.Step(id, Active)
	if err := f(); err != nil {
		p.Step(id, Failed)
		return err
	}
	p.Step(id, Done)
	return nil
}

func cancelled(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return uerr("cancelled", ctx.Err())
	}
	return err
}

// Install installs the station files and the boot hook on the speaker at ip.
func (r *Runner) Install(ctx context.Context, ip string, p Progress) (Result, error) {
	res, err := r.install(ctx, ip, p)
	if err != nil {
		err = cancelled(ctx, err)
		var ue *UserError
		if !errors.As(err, &ue) {
			err = uerr("internal", err)
		}
	}
	return res, err
}

func (r *Runner) install(ctx context.Context, ip string, p Progress) (Result, error) {
	c := r.cfg.Client
	t := r.cfg.Timing
	var res Result
	res.PageURL = c.PageURL(ip)

	// 1. API backup
	err := run(p, "backup_api", func() error {
		// The speaker may still be restarting (e.g. right after an uninstall): wait for it.
		s, err := c.Info(ctx, ip)
		for deadline := time.Now().Add(t.ConsoleWait); err != nil && time.Now().Before(deadline); {
			p.Log("speaker not answering yet, waiting: " + err.Error())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(t.ConsoleRetry):
			}
			s, err = c.Info(ctx, ip)
		}
		if err != nil {
			return uerr("backup_api", err)
		}
		dir := filepath.Join(r.cfg.BackupRoot, BackupDirName(s, r.cfg.Now()))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return uerr("backup_dir", err, dir)
		}
		os.Chmod(r.cfg.BackupRoot, 0o700)
		os.Chmod(dir, 0o700)
		res.BackupDir = dir
		for _, name := range []string{"info", "presets", "sources"} {
			b, err := c.Get(ctx, ip, name)
			if err != nil {
				return uerr("backup_api", err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".xml"), b, 0o600); err != nil {
				return uerr("backup_dir", err, dir)
			}
		}
		p.Log("API backup saved in " + dir)
		return nil
	})
	if err != nil {
		return res, err
	}

	// 2. temporary file server
	var srv *fileserver.Server
	var srvURL string
	err = run(p, "fileserver", func() error {
		local, err := speaker.LocalIPTowards(ip)
		if err != nil {
			return uerr("localip", err)
		}
		srv = fileserver.New(r.cfg.Files, res.BackupDir, p.Log)
		if err := srv.Start(r.cfg.ListenAddr); err != nil {
			return uerr("fileserver", err)
		}
		srvURL = "http://" + net.JoinHostPort(local, strconv.Itoa(srv.Port()))
		if err := hook.Validate(hook.Install(srvURL)); err != nil {
			return uerr("internal", err)
		}
		p.Log("file server at " + srvURL)
		return nil
	})
	if srv != nil {
		defer srv.Close()
	}
	if err != nil {
		return res, err
	}

	// 3. install hook + reboot
	err = run(p, "hook", func() error {
		out, err := speaker.RunCommands(ctx, c.ConsoleAddr(ip), t.CmdDelay, hook.InstallCommands(srvURL))
		p.Log("console: " + out)
		if errors.Is(err, speaker.ErrHookRejected) {
			return uerr("rejected", err)
		}
		if err != nil {
			return uerr("console", err)
		}
		return nil
	})
	if err != nil {
		return res, err
	}

	// 4. wait for the speaker to download the files and upload done.txt
	err = run(p, "restart", func() error {
		timer := time.NewTimer(t.UploadTimeout)
		defer timer.Stop()
		every := t.StallTimeout / 4
		if every <= 0 || every > time.Second {
			every = time.Second
		}
		tick := time.NewTicker(every)
		defer tick.Stop()
		restarts := 0
	wait:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return uerr("timeout_upload", fmt.Errorf("no upload after %v", t.UploadTimeout))
			case <-srv.Done():
				break wait
			case <-tick.C:
				// Seen on a SoundTouch 10: on the first boot after installing, the hook
				// sometimes stops after the first download. Another reboot fixes it.
				first := srv.FirstDownload()
				if t.StallTimeout <= 0 || first.IsZero() || time.Since(first) < t.StallTimeout || restarts >= t.MaxRestarts {
					continue
				}
				restarts++
				srv.ResetFirstDownload()
				p.Log(fmt.Sprintf("speaker stopped during the downloads, restarting it again (%d/%d)", restarts, t.MaxRestarts))
				if _, err := speaker.RunCommands(ctx, c.ConsoleAddr(ip), t.CmdDelay, []string{"sys reboot"}); err != nil {
					p.Log("restart failed: " + err.Error())
				}
				timer.Reset(t.UploadTimeout)
			}
		}
		res.TarReceived = srv.Received("speaker-nv.tar") > 0
		return nil
	})
	srv.Close()
	if err != nil {
		return res, err
	}

	// 5. replace the install hook by the runtime hook (no reboot)
	err = run(p, "finalize", func() error {
		deadline := time.Now().Add(t.ConsoleWait)
		for {
			out, err := speaker.RunCommands(ctx, c.ConsoleAddr(ip), t.CmdDelay, hook.FinalizeCommands())
			p.Log("console: " + out)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Now().After(deadline) {
				return uerr("finalize", err)
			}
			p.Log("console not ready, retrying: " + err.Error())
			if err := sleep(ctx, t.ConsoleRetry); err != nil {
				return err
			}
		}
	})
	if err != nil {
		return res, err
	}

	// 6. wait for the registry to be loaded and the page to answer
	err = run(p, "wait", func() error {
		if err := sleep(ctx, t.FinalWait); err != nil {
			return err
		}
		deadline := time.Now().Add(t.PageTimeout)
		for {
			if c.PageUp(ctx, ip) {
				res.PageUp = true
				return nil
			}
			if time.Now().After(deadline) {
				return nil // reported as a warning on the success screen
			}
			if err := sleep(ctx, t.ConsoleRetry); err != nil {
				return err
			}
		}
	})
	return res, err
}

// Uninstall restores the original Bose URLs and reboots the speaker.
func (r *Runner) Uninstall(ctx context.Context, ip string, p Progress) error {
	err := run(p, "un_send", func() error {
		t := r.cfg.Timing
		out, err := speaker.RunCommands(ctx, r.cfg.Client.ConsoleAddr(ip), t.CmdDelay, hook.UninstallCommands())
		// The speaker may still be restarting: retry connecting for a while.
		for deadline := time.Now().Add(t.ConsoleWait); err != nil && out == "" && time.Now().Before(deadline); {
			p.Log("speaker not answering yet, waiting: " + err.Error())
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(t.ConsoleRetry):
			}
			out, err = speaker.RunCommands(ctx, r.cfg.Client.ConsoleAddr(ip), t.CmdDelay, hook.UninstallCommands())
		}
		p.Log("console: " + out)
		if err != nil {
			return uerr("uninstall", err)
		}
		return nil
	})
	if err != nil {
		return cancelled(ctx, err)
	}
	return nil
}
