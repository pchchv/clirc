package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lrstanley/girc"
	"github.com/muesli/reflow/wordwrap"
)

var styleDim = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))

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

type formCfg struct {
	Name    string
	Nick    string
	Address string
	TLS     bool
	Chans   []string
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

func (addServerItem) Title() string {
	return "+ Add New Server"
}

func (addServerItem) Description() string {
	return ""
}

func (addServerItem) FilterValue() string {
	return ""
}

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

func (m model) Init() tea.Cmd {
	m.formInputs[m.formSel].Focus()
	return textinput.Blink
}

func (m model) addListItem(it serverEntry) model {
	var items []list.Item
	for _, existing := range m.serverList.Items() {
		if _, ok := existing.(addServerItem); ok {
			continue // skip placeholder, append later
		}

		se, ok := existing.(serverEntry)
		if ok && se.id == it.id && se.channel == it.channel {
			return m
		}

		items = append(items, existing)
	}

	items = append(append(items, it), addServerItem{})
	m.serverList.SetItems(items)
	return m
}

func (m *model) calcListHeight(avail int) int {
	n := listLen(m.serverList)
	if n == 0 {
		n = 1
	}

	h := n*m.rowH + 1 // +1 small padding
	if h > avail {
		h = avail
	}

	// Ensure at least enough for one item
	if h < m.rowH+1 {
		h = m.rowH + 1
	}

	return h
}

func (m *model) resizeList() {
	h := m.calcListHeight(m.height - 6)
	m.serverList.SetSize(m.leftWidth-4, h)
}

func (m *model) refreshChat() {
	if m.activeID == 0 {
		return
	}

	s := m.servers[m.activeID]
	if s == nil {
		return
	}

	w := m.chatVP.Width
	if w <= 0 {
		w = 80
	}

	var logs []string
	if s.channelLogs != nil {
		logs = s.channelLogs[m.activeChan]
	}

	var b strings.Builder
	for _, ln := range logs {
		b.WriteString(wordwrap.String(ln, w) + "\n")
	}

	m.chatVP.SetContent(b.String())
	m.chatVP.GotoBottom()
}

func (m *model) applyChanLine(msg ircChanLineMsg) {
	if !m.ready {
		if s := m.servers[msg.id]; s != nil {
			s.queued = append(s.queued, msg)
		}

		return
	}

	if s, ok := m.servers[msg.id]; ok {
		if s.channelLogs == nil {
			s.channelLogs = make(map[string][]string)
		}

		ch := msg.channel
		if ch == "" {
			ch = "_sys"
		}

		s.channelLogs[ch] = append(s.channelLogs[ch], msg.line)
		if m.mode == modeChat && m.activeID == msg.id && m.activeChan == ch {
			m.refreshChat()
		}
	}
}

func (m *model) focusFormField(idx formField) tea.Cmd {
	if idx < 0 {
		idx = 0
	}

	if idx >= totalFields {
		idx = totalFields - 1
	}

	if m.formSel != fieldSubmit {
		m.formInputs[m.formSel].Blur()
	}

	m.formSel = idx
	if m.formSel != fieldSubmit {
		m.formInputs[m.formSel].Focus()
		return textinput.Blink
	}

	return nil
}

func (m *model) pushSysLine(id serverID, ch, txt string) {
	if s := m.servers[id]; s != nil {
		if s.channelLogs == nil {
			s.channelLogs = make(map[string][]string)
		}

		if ch == "" {
			ch = "_sys"
		}

		s.channelLogs[ch] = append(s.channelLogs[ch], styleDim.Render(txt))
	}
}

func (m *model) focusRight() {
	switch m.mode {
	case modeChat:
		m.chatInput.Focus()
	case modeForm:
		for i := range m.formInputs {
			m.formInputs[i].Blur()
		}

		if m.formSel != fieldSubmit {
			m.formInputs[m.formSel].Focus()
		}
	}
}

func (m *model) blurRight() {
	switch m.mode {
	case modeChat:
		m.chatInput.Blur()
	case modeForm:
		for i := range m.formInputs {
			m.formInputs[i].Blur()
		}
	}
}

func listLen(l list.Model) int {
	return len(l.Items())
}

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)
}
