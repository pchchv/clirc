package main

import (
	"log"
	"os"
)

type serverID int

type pane int

const (
	paneServers pane = iota
	paneRight
)

type rightMode int

const (
	modeForm rightMode = iota
	modeChat
)

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)
}
