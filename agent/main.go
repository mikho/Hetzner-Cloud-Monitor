// hdash-agent is a read-only monitoring agent for the Hetzner Dashboard.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"hdash-agent/internal/collect"
	"hdash-agent/internal/config"
	"hdash-agent/internal/history"
	"hdash-agent/internal/install"
	"hdash-agent/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		run()
	case "install":
		installCmd(os.Args[2:])
	case "uninstall":
		if err := install.Uninstall(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("hdash-agent removed")
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hdash-agent run | install [flags] | uninstall | version")
	os.Exit(2)
}

func run() {
	cfg, err := config.Load(config.ConfigPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hist := history.Open(config.HistoryPath)
	defer hist.Save()
	collector := collect.NewCollector(version, cfg.Docker, hist.Add)
	go collector.Run(ctx)

	if err := server.Serve(ctx, cfg, server.New(cfg, collector, hist)); err != nil {
		log.Fatal(err)
	}
}

func installCmd(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	secretFile := fs.String("secret-file", "", "file with the 64 hex character key; deleted after reading")
	listen := fs.String("listen", "", "IP address to listen on, empty for all")
	port := fs.Int("port", config.DefaultPort, "TCP port")
	allowFrom := fs.String("allow-from", "", "comma separated IPs or CIDRs that may connect, empty for any")
	docker := fs.Bool("docker", false, "monitor Docker (adds the agent to the docker group)")
	rotate := fs.Bool("rotate-cert", false, "create a new TLS certificate")
	fs.Parse(args)

	o := install.Options{SecretFile: *secretFile, RotateCert: *rotate}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			o.Listen = listen
		case "port":
			o.Port = port
		case "allow-from":
			list := []string{}
			for _, a := range strings.Split(*allowFrom, ",") {
				if a = strings.TrimSpace(a); a != "" {
					list = append(list, a)
				}
			}
			o.AllowFrom = &list
		case "docker":
			o.Docker = docker
		}
	})
	res, err := install.Install(version, runtime.GOARCH, o)
	if err != nil {
		log.Fatal("install failed: ", err)
	}
	b, _ := json.Marshal(res)
	fmt.Println(install.ResultPrefix + string(b))
}
