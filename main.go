package main

import (
	"log"
	"os"

	"github.com/lrstanley/girc"
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

type serverEntry struct {
	id          serverID
	tls         bool
	name        string
	nick        string
	address     string // host:port
	channel     string // list entry channel
	channels    []string
	channelLogs map[string][]string // channel => lines ("_sys" for system)
	joined      map[string]bool
	client      *girc.Client
	connected   bool
	queued      []ircChanLineMsg // buffered until UI sized
}

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)
}
