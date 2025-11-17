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

var (
	state   model
	program *tea.Program

	pink       = lipgloss.Color("#DB2777")
	darkPink   = lipgloss.Color("#ac215f")
	stylePink  = lipgloss.NewStyle().Foreground(pink)
	stylePinkB = stylePink.Bold(true)
	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
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

func (m *model) formConfig() (formCfg, error) {
	name := getTextInput(m, fieldName)
	addr := getTextInput(m, fieldAddr)
	if name == "" || addr == "" {
		return formCfg{}, fmt.Errorf("name and address required")
	}

	tlsStr := strings.ToLower(getTextInput(m, fieldTLS))
	tls := tlsStr == "true" || tlsStr == "1" || tlsStr == "yes"
	nick := getTextInput(m, fieldNick)
	if nick == "" {
		nick = "zuse"
	}

	var chans []string
	if c := getTextInput(m, fieldChans); c != "" {
		for _, ch := range strings.Split(c, ",") {
			ch = strings.TrimSpace(ch)
			if ch != "" {
				chans = append(chans, ch)
			}
		}
	}

	return formCfg{name, addr, nick, tls, chans}, nil
}

func (m *model) clearForm() {
	for i := range m.formInputs {
		m.formInputs[i].SetValue("")
		m.formInputs[i].Blur()
	}

	m.formSel = fieldName
	m.formInputs[m.formSel].Focus()
}

func (m *model) injectASCIIArt(id serverID) {
	ascii := styleDim.Render(`
     ______     __         __     ______     ______    
    /\  ___\   /\ \       /\ \   /\  == \   /\  ___\   
    \ \ \____  \ \ \____  \ \ \  \ \  __<   \ \ \____  
     \ \_____\  \ \_____\  \ \_\  \ \_\ \_\  \ \_____\ 
      \/_____/   \/_____/   \/_/   \/_/ /_/   \/_____/ 

	 joining...
`)

	s := m.servers[id]
	if s.channelLogs == nil {
		s.channelLogs = make(map[string][]string)
	}

	// Add to system log
	s.channelLogs["_sys"] = append(s.channelLogs["_sys"], ascii)

	// Add to all known channels
	for _, ch := range s.channels {
		s.channelLogs[ch] = append(s.channelLogs[ch], ascii)
	}

	// Refresh if we're viewing this server now
	if m.mode == modeChat && m.activeID == id {
		m.refreshChat()
	}
}

func listLen(l list.Model) int {
	return len(l.Items())
}

func getTextInput(m *model, f formField) string {
	return strings.TrimSpace(m.formInputs[f].Value())
}

func initialModel() model {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true

	// Unselected state
	delegate.Styles.NormalTitle = stylePink
	delegate.Styles.NormalDesc = styleDim

	// Selected state (black text on pink background)
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#000000")). // Black text
		Background(darkPink).                  // Pink background
		Bold(true)

	delegate.Styles.SelectedTitle = selectedStyle
	delegate.Styles.SelectedDesc = selectedStyle

	l := list.New([]list.Item{addServerItem{}}, delegate, 20, 10)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)
	l.SetShowStatusBar(false)

	rowH := delegate.Height() + delegate.Spacing()
	newTI := func(ph string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = ph
		ti.Prompt = stylePinkB.Render(" > ")
		ti.TextStyle = stylePink
		return ti
	}

	var inputs [totalFields]textinput.Model
	inputs[fieldName] = newTI("Friendly name (e.g. Rekt)")
	inputs[fieldAddr] = newTI("irc.example.net:6697")
	inputs[fieldTLS] = newTI("TLS? (true/false)")
	inputs[fieldNick] = newTI("MySuperNickname")
	inputs[fieldChans] = newTI("#chan1,#chan2")

	ci := textinput.New()
	ci.Prompt = stylePinkB.Render("> ")
	ci.TextStyle = stylePink
	ci.Placeholder = "Type message or /command…"

	return model{
		leftWidth:  24,
		focus:      paneRight,
		mode:       modeForm,
		serverList: l,
		rowH:       rowH,
		servers:    map[serverID]*serverEntry{},
		nextID:     1,
		formInputs: inputs,
		chatInput:  ci,
	}
}

func main() {
	f, _ := os.CreateTemp("", "zuse.log")
	log.SetOutput(f)

	state = initialModel()
	program = tea.NewProgram(state, tea.WithAltScreen())
}
