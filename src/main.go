package main

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/apimgr/cairnbin/src/app"
	"github.com/apimgr/cairnbin/src/config"
)

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		log.Print(err)
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			os.Exit(exitCoder.ExitCode())
		}
		os.Exit(1)
	}
	cfg.Version = Version
	cfg.CommitID = CommitID
	cfg.BuildEpoch = BuildEpoch
	cfg.OfficialSite = OfficialSite
	if err := app.Run(context.Background(), *cfg); err != nil {
		log.Print(err)
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			os.Exit(exitCoder.ExitCode())
		}
		os.Exit(1)
	}
}
