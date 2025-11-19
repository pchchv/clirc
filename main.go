package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

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

	pink          = lipgloss.Color("#DB2777")
	darkPink      = lipgloss.Color("#ac215f")
	stylePink     = lipgloss.NewStyle().Foreground(pink)
	stylePinkB    = stylePink.Bold(true)
	styleDarkPink = lipgloss.NewStyle().Foreground(lipgloss.Color("#ac215f"))
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	styleDarkSel  = lipgloss.NewStyle().Foreground(lipgloss.Color("#000")).Background(darkPink)
	titleStyle    = lipgloss.NewStyle().Background(darkPink).Foreground(lipgloss.Color("#000000")).Bold(true).Padding(0, 1)
	box           = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(pink)
)

const (
	paneRight pane = iota
	paneServers

	modeForm rightMode = iota
	modeChat

	fieldTLS formField = iota
	fieldName
	fieldAddr
	fieldNick
	fieldChans
	fieldSubmit
	totalFields
)

type pane int

type rightMode int

type formField int

type errMsg error

type serverID int

type connectedMsg serverID

type addListItemMsg struct {
	item serverEntry
}

type disconnectedMsg struct {
	id  serverID
	err error
}

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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		leftInnerW := m.leftWidth - 2
		rightInnerW := (m.width - m.leftWidth) - 2
		innerH := m.height - 2

		// resize inputs
		for i := range m.formInputs {
			m.formInputs[i].Width = rightInnerW - 4
		}

		// list height calc
		listH := m.calcListHeight(innerH - 4)
		m.serverList.SetSize(leftInnerW-2, listH)
		m.headerLines = 2
		chatReserved := m.headerLines + 1 + 1
		m.chatVP.Width = rightInnerW - 2
		m.chatVP.Height = innerH - chatReserved - 1
		m.chatInput.Width = m.chatVP.Width
		m.ready = true
		// flush queued
		for _, s := range m.servers {
			for _, q := range s.queued {
				m.applyChanLine(q)
			}
			s.queued = nil
		}

		if m.mode == modeChat && m.activeID != 0 {
			m.refreshChat()
		}

		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "left":
			m.focus = paneServers
			m.blurRight()
			return m, nil
		case "right":
			m.focus = paneRight
			m.focusRight()
			return m, nil
		}

		if m.focus == paneServers {
			return m.updateServersPane(msg)
		}

		return m.updateRightPane(msg)
	case ircChanLineMsg:
		m.applyChanLine(msg)
		return m, nil
	case connectedMsg:
		if s, ok := m.servers[serverID(msg)]; ok {
			s.connected = true
			m.pushSysLine(s.id, "", "-- connected --")
			if m.mode == modeChat && m.activeID == serverID(msg) {
				m.refreshChat()
			}
		}
		return m, nil
	case disconnectedMsg:
		if s, ok := m.servers[msg.id]; ok {
			s.connected = false
			txt := "-- disconnected --"
			if msg.err != nil {
				txt += " (" + msg.err.Error() + ")"
			}

			m.pushSysLine(s.id, "", txt)
			if m.mode == modeChat && m.activeID == msg.id {
				m.refreshChat()
			}
		}
		return m, nil
	case addListItemMsg:
		m = m.addListItem(msg.item)
		m.resizeList() // ensure height fits new list
		return m, nil
	case errMsg:
		log.Println("error:", msg)
		return m, nil
	}

	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "loading…"
	}

	topPadding := 2
	serversTitle := styleDim.Render("Servers List")
	leftInner := lipgloss.JoinVertical(
		lipgloss.Left,
		titleStyle.Render("zuse irc beta"),
		lipgloss.NewStyle().MarginTop(1).MarginBottom(1).Render(serversTitle),
		m.serverList.View(),
	)
	leftBox := box.Width(m.leftWidth).Height(m.height - topPadding).Render(leftInner)

	var rightInner string
	switch m.mode {
	case modeForm:
		rightInner = m.viewForm()
	case modeChat:
		rightInner = m.viewChat()
	}

	rightBox := box.Width(m.width - m.leftWidth - 4).Height(m.height - topPadding).Render(rightInner)
	spacer := lipgloss.NewStyle().
		Width(2).
		Height(m.height - topPadding).
		Render(" ")
	joined := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox, spacer)
	topSpacer := lipgloss.NewStyle().
		Width(m.width).
		Height(topPadding).
		Render(strings.Repeat("\n", topPadding))
	finalView := lipgloss.JoinVertical(lipgloss.Left, topSpacer, joined)

	return lipgloss.Place(m.width, m.height, 0, 0, finalView)
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

func (m model) updateServersPane(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "enter":
		if listLen(m.serverList) == 0 {
			return m, nil
		}

		switch selected := m.serverList.SelectedItem().(type) {
		case serverEntry:
			m.activeID = selected.id
			if selected.channel != "" {
				m.activeChan = selected.channel
			} else {
				m.activeChan = "_sys"
			}

			var cmds []tea.Cmd
			s := m.servers[selected.id]
			if s.client == nil || !s.connected {
				cmds = append(cmds, connectServerCmd(selected.id))
			} else if selected.channel != "" && !s.joined[selected.channel] {
				s.client.Cmd.Join(selected.channel)
				if s.joined == nil {
					s.joined = map[string]bool{}
				}
				s.joined[selected.channel] = true
			}

			m.mode = modeChat
			m.focus = paneRight
			m.focusRight()
			m.refreshChat()
			return m, tea.Batch(cmds...)
		case addServerItem:
			m.mode = modeForm
			m.focus = paneRight
			m.clearForm()
			m.focusRight()
			return m, nil
		}
	case "a": // shortcut to add
		m.mode = modeForm
		m.focus = paneRight
		m.clearForm()
		m.focusRight()
		return m, nil
	case "d": // delete selected server entry
		if listLen(m.serverList) == 0 {
			return m, nil
		}

		switch item := m.serverList.SelectedItem().(type) {
		case serverEntry:
			id := item.id
			if s, ok := m.servers[id]; ok && s.client != nil {
				s.client.Quit("bye")
				s.client.Close()
			}
			delete(m.servers, id)

			var remaining []list.Item
			for _, it := range m.serverList.Items() {
				switch e := it.(type) {
				case serverEntry:
					if e.id != id {
						remaining = append(remaining, e)
					}
				case addServerItem:
					// re-add after loop
				}
			}
			remaining = append(remaining, addServerItem{})
			m.serverList.SetItems(remaining)
			m.resizeList()

			if m.activeID == id {
				m.mode = modeForm
				m.activeID = 0
				m.activeChan = ""
			}

			return m, nil
		}
	}

	var cmd tea.Cmd
	m.serverList, cmd = m.serverList.Update(key)
	return m, cmd
}

func (m model) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up":
		if m.formSel > 0 {
			return m, m.focusFormField(m.formSel - 1)
		}
	case "down":
		if m.formSel < totalFields-1 {
			return m, m.focusFormField(m.formSel + 1)
		}
	case "enter":
		if m.formSel < fieldSubmit {
			return m, m.focusFormField(m.formSel + 1)
		}
		cfg, err := m.formConfig()
		if err != nil {
			m.formInputs[fieldSubmit].SetValue("error: " + err.Error())
			return m, nil
		}
		id := m.nextID
		m.nextID++

		s := &serverEntry{
			id:          id,
			name:        cfg.Name,
			address:     cfg.Address,
			tls:         cfg.TLS,
			nick:        cfg.Nick,
			channels:    cfg.Chans,
			channelLogs: make(map[string][]string),
			joined:      make(map[string]bool),
		}
		m.servers[id] = s
		m.injectASCIIArt(id)

		var cmds []tea.Cmd
		if len(cfg.Chans) > 0 {
			for i := len(cfg.Chans) - 1; i >= 0; i-- {
				ch := cfg.Chans[i]
				copy := *s
				copy.channel = ch
				cmds = append(cmds, addListItemCmd(copy))
			}
		} else {
			cmds = append(cmds, addListItemCmd(*s))
		}

		m.activeID = id
		if len(cfg.Chans) > 0 {
			m.activeChan = cfg.Chans[0]
		} else {
			m.activeChan = "_sys"
		}

		m.mode = modeChat
		m.focusRight()
		cmds = append(cmds, connectServerCmd(id), textinput.Blink)
		return m, tea.Batch(cmds...)
	}

	if m.formSel != fieldSubmit {
		var cmd tea.Cmd
		m.formInputs[m.formSel], cmd = m.formInputs[m.formSel].Update(key)
		return m, cmd
	}

	return m, nil
}

func (m model) updateChat(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "up":
		m.chatVP.ScrollUp(1)
	case "down":
		m.chatVP.ScrollDown(1)
	case "pgup":
		m.chatVP.HalfPageUp()
	case "pgdown":
		m.chatVP.HalfPageDown()
	case "enter":
		txt := strings.TrimSpace(m.chatInput.Value())
		if txt == "" {
			return m, nil
		}
		m.chatInput.SetValue("")
		s := m.servers[m.activeID]

		if strings.HasPrefix(txt, "/") {
			return m, m.handleSlash(s, txt)
		}

		if m.activeChan == "" || m.activeChan == "_sys" {
			m.pushSysLine(s.id, "_sys", "-- no channel selected, use /join #chan or select an item --")
			m.refreshChat()
			return m, nil
		}

		if s.client != nil {
			s.client.Cmd.Message(m.activeChan, txt)
		}
		line := styleDarkPink.Render(
			fmt.Sprintf("[%s] <%s> %s", time.Now().Format("15:04"), s.nick, txt),
		)
		return m, sendChanLineCmd(s.id, m.activeChan, line)
	}

	var cmd tea.Cmd
	m.chatInput, cmd = m.chatInput.Update(key)
	return m, cmd
}

func (m model) updateRightPane(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeForm:
		return m.updateForm(key)
	case modeChat:
		return m.updateChat(key)
	default:
		return m, nil
	}
}

func (m model) handleSlash(s *serverEntry, raw string) tea.Cmd {
	var arg string
	parts := strings.SplitN(strings.TrimPrefix(raw, "/"), " ", 2)
	if len(parts) == 2 {
		arg = parts[1]
	}

	logSys := func(t string) {
		m.pushSysLine(s.id, m.activeChan, t)
		m.refreshChat()
	}

	cmd := strings.ToLower(parts[0])
	switch cmd {
	case "join":
		if arg == "" {
			logSys("usage: /join #chan")
			return nil
		}

		if s.client != nil && s.connected {
			s.client.Cmd.Join(arg)
		}

		if !contains(s.channels, arg) {
			s.channels = append(s.channels, arg)
		}

		if s.joined == nil {
			s.joined = map[string]bool{}
		}

		s.joined[arg] = true

		// inject ASCII for the new channel too
		ascii := styleDim.Render("─── Chat initialized ───")
		s.channelLogs[arg] = append(s.channelLogs[arg], ascii)

		logSys("-- joined " + arg + " --")

		copy := *s
		copy.channel = arg
		return addListItemCmd(copy)
	case "nick":
		if arg == "" {
			logSys("usage: /nick newnick")
			return nil
		}

		if s.client != nil {
			s.client.Cmd.Nick(arg)
		}

		logSys("-- nick change requested: " + arg)
		return nil
	case "quit":
		if s.client != nil {
			s.client.Quit("bye")
		}
		return nil
	case "msg":
		p := strings.SplitN(arg, " ", 2)
		if len(p) < 2 {
			logSys("usage: /msg target text")
			return nil
		}

		target, text := p[0], p[1]
		if s.client != nil {
			s.client.Cmd.Message(target, text)
		}

		logSys(fmt.Sprintf("[to %s] %s", target, text))
		return nil
	default:
		logSys("unknown command: " + cmd)
		return nil
	}
}

func (m model) viewForm() string {
	labels := []string{
		" Custom Server Name ",
		" Server:Port ",
		" TLS ",
		" Nick / Username / Real ",
		" Channels (comma) ",
		" SUBMIT ",
	}

	var b strings.Builder
	b.WriteString(stylePinkB.Render(" ↈ  Add New IRC Connection") + "\n\n")
	for i := 0; i < int(totalFields); i++ {
		label := labels[i]
		if i == int(m.formSel) && m.focus == paneRight {
			label = styleDarkSel.Render(label)
		} else {
			label = stylePink.Render(label)
		}

		if i == int(fieldSubmit) {
			b.WriteString(label + "\n\n")
		} else {
			b.WriteString(label + "\n" + m.formInputs[i].View() + "\n\n")
		}
	}

	b.WriteString(styleDim.Render("↑/↓ fields · Enter submit · ←/→ panes"))
	return b.String()
}

func (m model) viewChat() string {
	var header strings.Builder
	title := "Chat"
	if s, ok := m.servers[m.activeID]; ok {
		stat := "●"
		if !s.connected {
			stat = "○"
		}

		chanLabel := m.activeChan
		if chanLabel == "_sys" || chanLabel == "" {
			chanLabel = "(system)"
		}

		title = fmt.Sprintf("%s %s (%s) %s", stat, s.name, s.nick, chanLabel)
	}

	header.WriteString(stylePinkB.Render(title) + "\n")
	header.WriteString(titleStyle.Render("↑/↓ scroll · ←/→ panes") + "\n")
	div := stylePink.Render(strings.Repeat("─", m.chatVP.Width))
	return lipgloss.JoinVertical(
		lipgloss.Left,
		header.String()+m.chatVP.View(),
		div,
		m.chatInput.View(),
	)
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

	// ensure at least enough for one item
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

	// add to system log
	s.channelLogs["_sys"] = append(s.channelLogs["_sys"], ascii)

	// add to all known channels
	for _, ch := range s.channels {
		s.channelLogs[ch] = append(s.channelLogs[ch], ascii)
	}

	// refresh if we're viewing this server now
	if m.mode == modeChat && m.activeID == id {
		m.refreshChat()
	}
}

func connectServerCmd(id serverID) tea.Cmd {
	return func() tea.Msg {
		s := state.servers[id]
		if s == nil {
			return errMsg(fmt.Errorf("server not found"))
		}

		host, portStr, err := net.SplitHostPort(s.address)
		if err != nil {
			return errMsg(fmt.Errorf("invalid server address: %w", err))
		}

		port, err := strconv.Atoi(portStr)
		if err != nil {
			return errMsg(fmt.Errorf("invalid port: %w", err))
		}

		cfg := girc.Config{
			Server: host,
			Port:   port,
			Nick:   s.nick,
			User:   s.nick,
			Name:   s.nick,
			SSL:    s.tls,
		}
		c := girc.New(cfg)

		// Connected / Disconnected
		c.Handlers.Add(girc.CONNECTED, func(cl *girc.Client, _ girc.Event) {
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: styleDim.Render("-- connected to " + s.address + " --")})
			for _, ch := range s.channels {
				cl.Cmd.Join(ch)
			}
			program.Send(connectedMsg(id))
		})
		c.Handlers.Add(girc.DISCONNECTED, func(cl *girc.Client, _ girc.Event) {
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: styleDim.Render("-- disconnected --")})
			program.Send(disconnectedMsg{id: id, err: nil})
		})

		// PRIVMSG
		c.Handlers.Add(girc.PRIVMSG, func(_ *girc.Client, e girc.Event) {
			if len(e.Params) < 2 {
				return
			}

			target := e.Params[0]
			text := e.Params[1]
			ch := dispatchTarget(s, target)
			line := stylePink.Render(
				fmt.Sprintf("[%s] <%s> %s", time.Now().Format("15:04"), e.Source.Name, text),
			)
			program.Send(ircChanLineMsg{id: id, channel: ch, line: line})
			if ch != "_sys" {
				program.Send(ircChanLineMsg{id: id, channel: "_sys", line: line})
			}
		})

		// ACTION (/me)
		c.Handlers.Add(girc.CTCP_ACTION, func(_ *girc.Client, e girc.Event) {
			if len(e.Params) < 2 {
				return
			}

			target := e.Params[0]
			text := e.Params[1]
			ch := dispatchTarget(s, target)
			line := fmt.Sprintf("[%s] * %s %s", time.Now().Format("15:04"), e.Source.Name, text)
			program.Send(ircChanLineMsg{id: id, channel: ch, line: styleDim.Render(line)})
			if ch != "_sys" {
				program.Send(ircChanLineMsg{id: id, channel: "_sys", line: styleDim.Render(line)})
			}
		})

		// NOTICE
		c.Handlers.Add(girc.NOTICE, func(_ *girc.Client, e girc.Event) {
			if len(e.Params) < 2 {
				return
			}

			target := e.Params[0]
			text := e.Params[1]
			ch := dispatchTarget(s, target)
			line := fmt.Sprintf("[%s] -NOTICE- %s", time.Now().Format("15:04"), text)
			program.Send(ircChanLineMsg{id: id, channel: ch, line: styleDim.Render(line)})
			if ch != "_sys" {
				program.Send(ircChanLineMsg{id: id, channel: "_sys", line: styleDim.Render(line)})
			}
		})

		// JOIN/PART/QUIT
		c.Handlers.Add(girc.JOIN, func(_ *girc.Client, e girc.Event) {
			ch := e.Params[0]
			line := fmt.Sprintf("[%s] * %s joined %s", time.Now().Format("15:04"), e.Source.Name, ch)
			program.Send(ircChanLineMsg{id: id, channel: ch, line: styleDim.Render(line)})
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: styleDim.Render(line)})
			if s.joined == nil {
				s.joined = map[string]bool{}
			}
			s.joined[ch] = true
		})
		c.Handlers.Add(girc.PART, func(_ *girc.Client, e girc.Event) {
			ch := e.Params[0]
			line := fmt.Sprintf("[%s] * %s left %s", time.Now().Format("15:04"), e.Source.Name, ch)
			program.Send(ircChanLineMsg{id: id, channel: ch, line: styleDim.Render(line)})
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: styleDim.Render(line)})
		})
		c.Handlers.Add(girc.QUIT, func(_ *girc.Client, e girc.Event) {
			line := fmt.Sprintf("[%s] * %s quit", time.Now().Format("15:04"), e.Source.Name)
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: styleDim.Render(line)})
		})

		// Topic & Names
		c.Handlers.Add(girc.RPL_TOPIC, func(_ *girc.Client, e girc.Event) {
			if len(e.Params) < 3 {
				return
			}

			ch := e.Params[1]
			topic := e.Params[2]
			line := styleDim.Render("— topic: " + topic)
			program.Send(ircChanLineMsg{id: id, channel: ch, line: line})
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: line})
		})
		c.Handlers.Add(girc.RPL_TOPICWHOTIME, func(_ *girc.Client, e girc.Event) {
			if len(e.Params) < 4 {
				return
			}

			ch := e.Params[1]
			who := e.Params[2]
			ts := e.Params[3]
			line := styleDim.Render("— set by " + who + " @ " + ts)
			program.Send(ircChanLineMsg{id: id, channel: ch, line: line})
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: line})
		})
		c.Handlers.Add(girc.RPL_NAMREPLY, func(_ *girc.Client, e girc.Event) {
			// ignored or custom
		})
		c.Handlers.Add(girc.RPL_ENDOFNAMES, func(_ *girc.Client, e girc.Event) {
			if len(e.Params) < 2 {
				return
			}
			ch := e.Params[1]
			line := styleDim.Render("— end of names")
			program.Send(ircChanLineMsg{id: id, channel: ch, line: line})
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: line})
		})

		const RPL_STATSCONN = "250"
		for _, ev := range []string{
			girc.RPL_WELCOME,
			girc.RPL_YOURHOST,
			girc.RPL_CREATED,
			girc.RPL_MYINFO,
			girc.RPL_ISUPPORT,
			girc.RPL_BOUNCE,
			girc.RPL_LUSERCLIENT,
			girc.RPL_LUSEROP,
			girc.RPL_LUSERUNKNOWN,
			RPL_STATSCONN,
			girc.RPL_LOCALUSERS,
			girc.RPL_GLOBALUSERS,
			girc.RPL_MOTDSTART,
			girc.RPL_MOTD,
			girc.RPL_ENDOFMOTD,
			girc.ERR_NOMOTD,
		} {
			evCopy := ev
			c.Handlers.Add(evCopy, func(_ *girc.Client, e girc.Event) {
				text := strings.Join(e.Params, " ")
				line := styleDim.Render(fmt.Sprintf("[%s] %s", time.Now().Format("15:04"), text))
				program.Send(ircChanLineMsg{id: id, channel: "_sys", line: line})
			})
		}

		for _, ev := range []string{
			girc.CAP,
			girc.AUTHENTICATE,
			girc.RPL_SASLSUCCESS,
			girc.ERR_SASLFAIL,
		} {
			evCopy := ev
			c.Handlers.Add(evCopy, func(_ *girc.Client, e girc.Event) {
				text := strings.Join(e.Params, " ")
				line := styleDim.Render(fmt.Sprintf("[%s] %s %s", time.Now().Format("15:04"), e.Command, text))
				program.Send(ircChanLineMsg{id: id, channel: "_sys", line: line})
			})
		}

		ignoreNumerics := map[string]bool{
			"315": true, // RPL_ENDOFWHO
			"352": true, // RPL_WHOREPLY
			"354": true, // WHOX reply
			"b09": true, // custom
		}

		c.Handlers.Add(girc.ALL_EVENTS, func(_ *girc.Client, e girc.Event) {
			// is numeric?
			if _, err := strconv.Atoi(e.Command); err != nil {
				return
			}

			if ignoreNumerics[e.Command] {
				return
			}

			txt := strings.Join(e.Params, " ")
			dest := "_sys"
			for _, p := range e.Params {
				if strings.HasPrefix(p, "#") {
					dest = p
					break
				}
			}

			line := styleDim.Render(fmt.Sprintf("[%s] %s", time.Now().Format("15:04"), txt))
			program.Send(ircChanLineMsg{id: id, channel: dest, line: line})
			if dest != "_sys" {
				program.Send(ircChanLineMsg{id: id, channel: "_sys", line: line})
			}
		})

		s.client = c
		if err := c.Connect(); err != nil {
			program.Send(ircChanLineMsg{id: id, channel: "_sys", line: "Connect error: " + err.Error()})
			return errMsg(err)
		}

		return nil
	}
}

func listLen(l list.Model) int {
	return len(l.Items())
}

func getTextInput(m *model, f formField) string {
	return strings.TrimSpace(m.formInputs[f].Value())
}

// Helper: decide log target.
func dispatchTarget(s *serverEntry, target string) string {
	if strings.HasPrefix(target, "#") {
		return target
	}
	return "_sys"
}

func sendChanLineCmd(id serverID, ch, line string) tea.Cmd {
	return func() tea.Msg {
		return ircChanLineMsg{
			id:      id,
			channel: ch,
			line:    line,
		}
	}
}

func addListItemCmd(it serverEntry) tea.Cmd {
	return func() tea.Msg {
		return addListItemMsg{item: it}
	}
}

func contains(sl []string, s string) bool {
	for _, v := range sl {
		if v == s {
			return true
		}
	}
	return false
}

func initialModel() model {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = true

	// unselected state
	delegate.Styles.NormalTitle = stylePink
	delegate.Styles.NormalDesc = styleDim

	// selected state (black text on pink background)
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#000000")). // black text
		Background(darkPink).                  // pink background
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
	if _, err := program.Run(); err != nil {
		fmt.Println("error:", err)
	}
}
