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

type formField int

const (
	fieldName formField = iota
	fieldAddr
	fieldTLS
	fieldNick
	fieldChans
	fieldSubmit
	totalFields
)

type ircChanLineMsg struct {
	id      serverID
	channel string
	line    string
}

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)
}
