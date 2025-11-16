package main

import (
	"fmt"
	"log"
	"os"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
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

func (s serverEntry) Title() string {
	if s.channel != "" {
		return fmt.Sprintf("%s · %s", s.name, s.channel)
	}

	return s.name
}

func (s serverEntry) Description() string {
	return s.address
}

func (s serverEntry) FilterValue() string {
	return s.name + " " + s.address
}

type addServerItem struct{}

type model struct {
	width       int
	height      int
	rowH        int // rows per item (delegate height + spacing)
	leftWidth   int
	headerLines int
	focus       pane
	mode        rightMode
	serverList  list.Model
	servers     map[serverID]*serverEntry
	nextID      serverID
	formInputs  [totalFields]textinput.Model
	formSel     formField
	activeID    serverID
	activeChan  string
	chatVP      viewport.Model
	chatInput   textinput.Model
	ready       bool
}

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)
}
