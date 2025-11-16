package main

import (
	"log"
	"os"
)

type pane int

const (
	paneServers pane = iota
	paneRight
)

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)
}
