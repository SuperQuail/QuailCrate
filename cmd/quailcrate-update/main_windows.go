package main

import (
	"os"
	"strconv"

	"golang.org/x/sys/windows"
	"quailcrate/internal/update"
)

var releaseVersion string
var releaseUpdatePublicKey string

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	parent, err := strconv.ParseUint(os.Args[2], 10, 64)
	if err != nil || parent == 0 {
		os.Exit(2)
	}
	if err := update.RunInstaller(os.Args[1], windows.Handle(parent), releaseVersion, update.ReleasePublicKey(releaseUpdatePublicKey)); err != nil {
		os.Exit(1)
	}
}
