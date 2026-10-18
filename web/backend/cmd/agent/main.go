package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"server-monitor/internal/agenttransport"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("agent: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: server-monitor-agent enroll|run|check|config-url|version [options]")
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return errors.New("server-monitor-agent supports Linux and Windows amd64/arm64")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	defaultConfig := "/etc/server-monitor-agent/config.json"
	if runtime.GOOS == "windows" {
		defaultConfig = filepath.Join(os.Getenv("ProgramData"), "ServerMonitorAgent", "config.json")
	}
	configPath := flags.String("config", defaultConfig, "agent configuration file")
	serverURL := flags.String("server-url", "", "HTTPS panel URL")
	ticket := flags.String("ticket", "", "single-use enrollment ticket (prefer --ticket-stdin)")
	stdinTicket := flags.Bool("ticket-stdin", false, "read the enrollment ticket from standard input")
	force := flags.Bool("force", false, "replace an existing enrollment")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "service":
		return runService(*configPath)
	case "enroll":
		if _, err := os.Stat(*configPath); err == nil && !*force {
			return errors.New("agent is already enrolled; use --force only to replace the enrollment")
		}
		if *stdinTicket {
			if *ticket != "" {
				return errors.New("use either --ticket or --ticket-stdin")
			}
			value, err := bufio.NewReader(io.LimitReader(os.Stdin, 4097)).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return errors.New("could not read enrollment ticket")
			}
			*ticket = strings.TrimSpace(value)
		}
		config, err := agenttransport.Enroll(ctx, *serverURL, *ticket, version)
		if err != nil {
			return err
		}
		if err := agenttransport.SaveConfig(*configPath, config); err != nil {
			return fmt.Errorf("could not save enrollment (register a new ticket before retrying): %w", err)
		}
		fmt.Printf("Agent enrolled: %s\n", config.AgentID)
		return nil
	case "run", "check", "config-url":
		config, err := agenttransport.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		if args[0] == "check" {
			fmt.Println("Agent configuration is valid")
			return nil
		}
		if args[0] == "config-url" {
			fmt.Println(config.ServerURL)
			return nil
		}
		agent := &agenttransport.Agent{Config: config, Version: version, Logger: log.Default()}
		err = agent.Run(ctx)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	default:
		return errors.New("unknown agent command")
	}
}
