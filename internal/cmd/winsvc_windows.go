//go:build windows

package cmd

import (
	"context"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

type winSvcHandler struct {
	run func(ctx context.Context)
}

func (h *winSvcHandler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.run(ctx)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

loop:
	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				break loop
			}
		case <-done:
			break loop
		}
	}

	<-done
	return false, 0
}

// redirectOutputToLogFile redirects stdout, stderr, and logrus to a log file
// next to the binary so the service produces no console window.
func redirectOutputToLogFile() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	logPath := filepath.Join(filepath.Dir(exe), "logs", "service.log")
	if err = os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	handle := windows.Handle(f.Fd())
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, handle)
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, handle)
	os.Stdout = f
	os.Stderr = f
	log.SetOutput(f)
}

func isWindowsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

func runAsWindowsService(name string, fn func(ctx context.Context)) error {
	redirectOutputToLogFile()
	return svc.Run(name, &winSvcHandler{run: fn})
}
