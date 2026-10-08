package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set at build time: -ldflags "-X main.buildVersion=... -X main.buildCommit=... -X main.buildDate=..."
var (
	buildVersion = "dev"
	buildCommit  = ""
	buildDate    = ""
)

func buildCommitOrVCS() string {
	if buildCommit != "" {
		return buildCommit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				return s.Value[:12]
			}
		}
	}
	return "unknown"
}

func buildInfoText() string {
	date := buildDate
	if date == "" {
		date = "unknown"
	}
	return fmt.Sprintf("deye-open-controller %s (commit %s, built %s, %s %s/%s)", buildVersion, buildCommitOrVCS(), date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
