package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"xusage/internal/server"
)

// version is overridden at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// flags holds the parsed command line.
type flags struct {
	addr string
	poll time.Duration
	open bool
	show bool
}

func parseFlags() (*flags, error) {
	f := &flags{}
	flag.StringVar(&f.addr, "addr", "127.0.0.1:8787", "HTTP bind address (loopback only)")
	flag.DurationVar(&f.poll, "poll", 2*time.Second, "background data refresh interval")
	flag.BoolVar(&f.open, "open", false, "open the dashboard in the default browser")
	flag.BoolVar(&f.show, "version", false, "print version and exit")
	flag.Parse()
	return f, nil
}

// run parses flags, starts the server, and returns the process exit error.
func run() error {
	f, err := parseFlags()
	if err != nil {
		return err
	}
	if f.show {
		fmt.Println(fullVersion())
		return nil
	}

	server.Version = version // surfaced by /api/meta and the dashboard footer

	return server.Run(server.Options{
		Addr:           f.addr,
		PollInterval:   f.poll,
		RefreshOnStart: true,
		OnReady: func(url string) {
			if f.open {
				openBrowser(url)
			}
			printStartup(url)
		},
	})
}

func osExit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "xusage:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
