// Command installer is the one-file GUI installer for SoundTouch Radio: it
// opens a page in the default browser that finds the speaker and installs the
// station files and boot hook on it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	localradio "github.com/nhouba/soundtouch-localradio"
	"github.com/nhouba/soundtouch-localradio/internal/flow"
	"github.com/nhouba/soundtouch-localradio/internal/i18n"
	"github.com/nhouba/soundtouch-localradio/internal/speaker"
	"github.com/nhouba/soundtouch-localradio/internal/ui"
)

// version is set at build time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	speakerIP := flag.String("speaker", "", "IP address of the speaker (skips the network search)")
	noBrowser := flag.Bool("no-browser", false, "do not open the web browser")
	port := flag.Int("port", 0, "port for the local page (default: any free port)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	lang := i18n.Pick(os.Getenv("LANG"))
	logger := log.New(os.Stderr, "", log.LstdFlags)

	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	client := speaker.NewClient(10 * time.Second)
	runner := flow.New(flow.Config{
		Client:     client,
		Files:      localradio.SpeakerFiles(),
		BackupRoot: filepath.Join(home, "SoundTouch-Backup"),
	})

	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not start the installer page:", err)
		waitForEnter()
		os.Exit(1)
	}
	addr := ln.Addr().String()
	url := "http://" + addr + "/"

	quit := make(chan struct{})
	srv := ui.New(ui.Options{
		Runner:     runner,
		Client:     client,
		ScanClient: speaker.NewClient(500 * time.Millisecond),
		AutoScan:   *speakerIP == "",
		Quit:       func() { close(quit) },
		Logf:       logger.Printf,
	}, addr)
	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go httpSrv.Serve(ln)

	if *speakerIP != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if _, uerr := srv.AddSpeaker(ctx, *speakerIP); uerr != nil {
			logger.Printf("%s %s", i18n.T(lang, "err."+uerr.Key+".text", uerr.Args...), i18n.T(lang, "err."+uerr.Key+".next"))
		}
		cancel()
	}

	fmt.Println(i18n.T(lang, "cli.started"), "("+version+")")
	fmt.Println(i18n.T(lang, "cli.open"))
	fmt.Println()
	fmt.Println("    " + url)
	fmt.Println()
	fmt.Println(i18n.T(lang, "cli.keep"))
	if !*noBrowser {
		if err := ui.OpenBrowser(url); err != nil {
			logger.Printf("could not open the browser: %v", err)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-quit:
	}
	srv.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
}

// waitForEnter keeps a double-clicked console window open so the error can be read.
func waitForEnter() {
	fmt.Fprintln(os.Stderr, "Press Enter to close.")
	fmt.Scanln()
}
