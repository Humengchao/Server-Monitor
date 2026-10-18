//go:build windows

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
	"server-monitor/internal/agenttransport"
)

type serviceHandler struct{ run func(context.Context) error }

func (h serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	status <- current
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			return false, 0
		case request, ok := <-requests:
			if !ok {
				cancel()
				<-done
				return false, 0
			}
			switch request.Cmd {
			case svc.Interrogate:
				status <- current
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 15000}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

func runService(configPath string) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return errors.New("service must be started by the Windows Service Control Manager; use run for foreground mode")
	}
	config, err := agenttransport.LoadConfig(configPath)
	if err != nil {
		return err
	}
	logPath := filepath.Join(filepath.Dir(configPath), "agent.log")
	if info, err := os.Stat(logPath); err == nil && info.Size() > 10*1024*1024 {
		_ = os.Remove(logPath + ".old")
		_ = os.Rename(logPath, logPath+".old")
	}
	output, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	agent := &agenttransport.Agent{Config: config, Version: version, Logger: log.New(output, "", log.LstdFlags)}
	return svc.Run("ServerMonitorAgent", serviceHandler{run: agent.Run})
}
