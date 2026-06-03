package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

var (
	showMessageID = flag.Bool("show_message_id", false, "Show message ID in a message.")
)

const (
	conversationViewHelp = `?, F1              — Help
u, ←               — Back to thread list
^N                 — Next thread
^P                 — Previous thread
enter              — Expand/collapse message
n, j, Down         — Scroll down / next message header
p, k, Up           — Scroll up / previous message header
Space, PgDn        — Page down
Backspace, PgUp    — Page up
e                  — Archive thread
d                  — Trash thread
l                  — Add label to thread
L                  — Remove label from thread
*                  — Toggle starred
r                  — Reply to last message
a                  — Reply all to last message
f                  — Forward last message
t                  — View attachments of focused message
q                  — Quit

Press [enter] to exit
`
)

// ConversationView displays all messages in a thread stacked vertically.
type ConversationView struct {
	thread *cmdg.Thread
	keys   *input.Input
	screen *display.Screen

	update chan struct{}
	errors chan error

	// View state
	scroll       int
	expandedMsgs map[int]bool // which message indices are expanded
	focusedMsg   int          // index of the focused message header
}

// NewConversationView creates a new conversation view.
func NewConversationView(ctx context.Context, thread *cmdg.Thread, in *input.Input) *ConversationView {
	cv := &ConversationView{
		thread:       thread,
		keys:         in,
		update:       make(chan struct{}, 1),
		errors:       make(chan error, 20),
		expandedMsgs: make(map[int]bool),
	}
	go func() {
		if err := thread.Preload(ctx, cmdg.LevelFull); err != nil {
			cv.errors <- err
			return
		}
		// Mark thread as read
		if err := thread.RemoveLabelID(ctx, cmdg.Unread); err != nil {
			log.Warningf("Failed to mark thread as read: %v", err)
		}
		// Expand the last message by default
		count := thread.MessageCount()
		if count > 0 {
			cv.expandedMsgs[count-1] = true
			cv.focusedMsg = count - 1
		}
		cv.update <- struct{}{}
	}()
	return cv
}

// wrapAddressHeader wraps a header line (like "    To: addr1, addr2, ...") at
// the given width, breaking at comma boundaries and indenting continuation
// lines to align with the first address.
func wrapAddressHeader(prefix, value string, width int) []string {
	if width <= 0 || len(prefix+value) <= width {
		return []string{prefix + value}
	}

	indent := strings.Repeat(" ", len(prefix))
	var lines []string
	remaining := value
	currentLine := prefix

	for remaining != "" {
		idx := strings.Index(remaining, ",")
		var segment string
		if idx == -1 {
			segment = remaining
			remaining = ""
		} else {
			segment = remaining[:idx+1] + " "
			remaining = strings.TrimLeft(remaining[idx+1:], " ")
		}

		if len(currentLine)+len(segment) > width && currentLine != prefix && currentLine != indent {
			lines = append(lines, strings.TrimRight(currentLine, " "))
			currentLine = indent + segment
		} else {
			currentLine += segment
		}
	}
	if strings.TrimSpace(currentLine) != "" {
		lines = append(lines, strings.TrimRight(currentLine, " "))
	}
	return lines
}

// renderLines renders the conversation into a slice of display lines.
func (cv *ConversationView) renderLines(ctx context.Context) []string {
	var lines []string

	// Thread subject header
	subj, err := cv.thread.Subject(ctx)
	if err != nil || subj == "" {
		subj = "(No subject)"
	}
	lines = append(lines, fmt.Sprintf("%s%s%s", display.Bold, subj, display.Reset))
	lines = append(lines, strings.Repeat("─", 60))

	count := cv.thread.MessageCount()
	for i := 0; i < count; i++ {
		msgs := cv.thread.Messages
		if i >= len(msgs) {
			break
		}
		msg := msgs[i]

		// Message header line
		from, _ := msg.GetFrom(ctx)
		if from == "" {
			from = "?"
		}
		date := ""
		if t, err := msg.GetOriginalTime(ctx); err == nil {
			date = t.Format("Jan 02, 2006 15:04")
		}

		headerPrefix := "▶"
		if cv.expandedMsgs[i] {
			headerPrefix = "▼"
		}
		focusMarker := "  "
		if i == cv.focusedMsg {
			focusMarker = display.Reverse + "→" + display.Reset + " "
		}

		headerLine := fmt.Sprintf("%s%s %s%s%s  %s",
			focusMarker, headerPrefix, display.Bold, from, display.Reset, date)
		lines = append(lines, headerLine)

		if cv.expandedMsgs[i] {
			// Show headers
			if *showMessageID {
				msgID, _ := msg.GetHeader(ctx, "Message-ID")
				if msgID != "" {
					lines = append(lines, fmt.Sprintf("    Message-ID: %s", msgID))
				}
			}
			screenWidth := 0
			if cv.screen != nil {
				screenWidth = cv.screen.Width
			}
			to, _ := msg.GetHeader(ctx, "To")
			if to != "" {
				lines = append(lines, wrapAddressHeader("    To: ", to, screenWidth)...)
			}
			cc, _ := msg.GetHeader(ctx, "CC")
			if cc != "" {
				lines = append(lines, wrapAddressHeader("    CC: ", cc, screenWidth)...)
			}
			lines = append(lines, "")

			// Message body
			body, err := msg.GetBody(ctx)
			if err != nil {
				lines = append(lines, fmt.Sprintf("    [Error loading body: %v]", err))
			} else {
				for _, bl := range strings.Split(body, "\n") {
					lines = append(lines, "    "+bl)
				}
			}
			lines = append(lines, "")
		}
		lines = append(lines, strings.Repeat("─", 60))
	}
	return lines
}

// Run runs the conversation view and returns a ThreadViewOp.
func (cv *ConversationView) Run(ctx context.Context) (*ThreadViewOp, error) {
	var err error
	cv.screen, err = display.NewScreen()
	if err != nil {
		return nil, err
	}
	defer func() {
		cv.screen.Clear()
		cv.screen.Draw()
	}()

	cv.screen.Printf(0, 0, "Loading conversation…")
	cv.screen.Draw()

	contentHeight := cv.screen.Height - 2

	draw := func() {
		lines := cv.renderLines(ctx)
		for n := 0; n < contentHeight; n++ {
			cur := n + cv.scroll
			if cur >= len(lines) {
				cv.screen.Printlnf(n, "")
				continue
			}
			cv.screen.Printlnf(n, "%s", lines[cur])
		}
		totalLines := len(lines)
		pct := 0
		if totalLines > 0 {
			pct = min(100, int(100*float64(cv.scroll+contentHeight)/float64(totalLines)))
		}
		cv.screen.Printlnf(cv.screen.Height-2, "%s", strings.Repeat("—", cv.screen.Width))
		cv.screen.Printlnf(cv.screen.Height-1, "Conversation: %d messages | %d%% | [?] help",
			cv.thread.MessageCount(), pct)
		cv.screen.Draw()
	}

	for {
		select {
		case <-cv.update:
			draw()
			continue
		case err := <-cv.errors:
			showError(cv.screen, cv.keys, err.Error())
			cv.screen.Draw()
			continue
		case <-cv.keys.Winch():
			cv.screen, err = display.NewScreen()
			if err != nil {
				return nil, err
			}
			contentHeight = cv.screen.Height - 2
		case key, ok := <-cv.keys.Chan():
			if !ok {
				continue
			}
			switch key {
			case "?", input.F1:
				if err := help(conversationViewHelp, cv.keys); err != nil {
					log.Infof("help() failed: %v", err)
				}
			case "u", input.Left:
				return nil, nil
			case input.CtrlN:
				return ThreadOpNext(), nil
			case input.CtrlP:
				return ThreadOpPrev(), nil
			case "q":
				return ThreadOpQuit(), nil
			case input.Enter:
				// Toggle expand/collapse of focused message
				cv.expandedMsgs[cv.focusedMsg] = !cv.expandedMsgs[cv.focusedMsg]
			case "N", "n", "j", input.Down:
				count := cv.thread.MessageCount()
				if cv.focusedMsg < count-1 {
					cv.focusedMsg++
				} else {
					cv.scroll++
				}
			case "P", "p", "k", input.Up:
				if cv.focusedMsg > 0 {
					cv.focusedMsg--
				} else if cv.scroll > 0 {
					cv.scroll--
				}
			case " ", input.PgDown:
				cv.scroll += contentHeight
			case input.Backspace, input.PgUp:
				cv.scroll -= contentHeight
				if cv.scroll < 0 {
					cv.scroll = 0
				}
			case "e":
				go func() {
					if err := cv.thread.RemoveLabelID(ctx, cmdg.Inbox); err != nil {
						cv.errors <- errors.Wrapf(err, "archiving thread")
					}
				}()
				return ThreadOpRemoveCurrent(nil), nil
			case "d":
				go func() {
					if err := cv.thread.Trash(ctx); err != nil {
						cv.errors <- errors.Wrapf(err, "trashing thread")
					}
				}()
				return ThreadOpRemoveCurrent(nil), nil
			case "*":
				if cv.thread.IsStarred() {
					go func() {
						if err := cv.thread.RemoveLabelID(ctx, cmdg.Starred); err != nil {
							cv.errors <- errors.Wrapf(err, "removing STARRED")
						}
					}()
				} else {
					go func() {
						if err := cv.thread.AddLabelID(ctx, cmdg.Starred); err != nil {
							cv.errors <- errors.Wrapf(err, "adding STARRED")
						}
					}()
				}
			case "l":
				var opts []*dialog.Option
				for _, l := range conn.Labels() {
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.Label,
					})
				}
				label, err := dialog.Selection(opts, "Label> ", false, cv.keys)
				if errors.Cause(err) == dialog.ErrAborted {
					// No-op.
				} else if err != nil {
					cv.errors <- errors.Wrapf(err, "Selecting label")
				} else {
					go func() {
						if err := cv.thread.AddLabelID(ctx, label.Key); err != nil {
							cv.errors <- errors.Wrapf(err, "labelling thread")
						}
					}()
				}
			case "L":
				var opts []*dialog.Option
				for _, l := range conn.Labels() {
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.Label,
					})
				}
				label, err := dialog.Selection(opts, "Label> ", false, cv.keys)
				if errors.Cause(err) == dialog.ErrAborted {
					// No-op.
				} else if err != nil {
					cv.errors <- errors.Wrapf(err, "Selecting label")
				} else {
					go func() {
						if err := cv.thread.RemoveLabelID(ctx, label.Key); err != nil {
							cv.errors <- errors.Wrapf(err, "unlabelling thread")
						}
					}()
				}
			case "r":
				msgs := cv.thread.Messages
				if cv.focusedMsg < len(msgs) {
					if err := reply(ctx, conn, cv.keys, msgs[cv.focusedMsg]); err != nil {
						cv.errors <- errors.Wrapf(err, "replying")
					}
				}
			case "a":
				msgs := cv.thread.Messages
				if cv.focusedMsg < len(msgs) {
					if err := replyAll(ctx, conn, cv.keys, msgs[cv.focusedMsg]); err != nil {
						cv.errors <- errors.Wrapf(err, "replying all")
					}
				}
			case "f":
				msgs := cv.thread.Messages
				if cv.focusedMsg < len(msgs) {
					if err := forward(ctx, conn, cv.keys, msgs[cv.focusedMsg]); err != nil {
						cv.errors <- errors.Wrapf(err, "forwarding")
					}
				}
			case "t":
				// View attachments of focused message
				msgs := cv.thread.Messages
				if cv.focusedMsg < len(msgs) {
					msg := msgs[cv.focusedMsg]
					attachments, err := msg.Attachments(ctx)
					if err != nil {
						cv.errors <- errors.Wrapf(err, "getting attachments")
					} else if len(attachments) > 0 {
						if err := listAttachments(ctx, cv.keys, msg); err != nil {
							cv.errors <- errors.Wrapf(err, "browsing attachments")
						}
					}
				}
			default:
				log.Infof("ConversationView got unknown key %q", key)
			}
		}
		draw()
	}
}
