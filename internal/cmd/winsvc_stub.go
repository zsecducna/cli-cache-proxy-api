//go:build !windows

package cmd

import "context"

func isWindowsService() bool { return false }

func runAsWindowsService(_ string, _ func(ctx context.Context)) error { return nil }
