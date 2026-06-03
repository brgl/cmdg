package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/ThomasHabets/cmdg/pkg/cmdg"
	"github.com/ThomasHabets/cmdg/pkg/dialog"
	"github.com/ThomasHabets/cmdg/pkg/display"
	"github.com/ThomasHabets/cmdg/pkg/input"
)

const (
	scrollLimit = 5
)

var (
	messageListReloadTimeout = 40 * time.Second
)

func help(txt string, keys *input.Input) error {
	screen, err := display.NewScreen()
	if err != nil {
		return err
	}
	lines := strings.Split(txt, "\n")
	maxlen := 0
	for _, l := range lines {
		if n := len(l); n > maxlen {
			maxlen = n
		}
	}
	screen.Printlnf(0, "%s", strings.Repeat("—", screen.Width))
	for n, l := range lines {
		screen.Printlnf(n+1, "%s%s", strings.Repeat(" ", (screen.Width-maxlen)/2), l)
	}
	for {
		screen.Draw()
		k := <-keys.Chan()
		switch k {
		case input.Enter:
			return nil
		}
	}
}

func showError(oscreen *display.Screen, keys *input.Input, msg string) {
	log.Warningf("Displaying error to user: %q", msg)

	screen := oscreen.Copy()
	lines := []string{
		strings.Repeat("—", screen.Width),
	}
	for len(msg) > 0 {
		this := msg
		if len(this) > screen.Width {
			this, msg = msg[:screen.Width], msg[screen.Width:]
		} else {
			msg = ""
		}
		lines = append(lines, this)
	}
	lines = append(lines, "Press [enter] to continue", lines[0])
	start := (screen.Height - len(lines)) / 2
	for n, l := range lines {
		screen.Printlnf(start+n, "%s%s", display.Red, l)
	}
	screen.Draw()
	for {
		if input.Enter == <-keys.Chan() {
			return
		}
	}
}

const (
	threadListViewHelp = `?, F1              — Help
enter, →           — Open conversation
space, x           — Mark thread and advance
X                  — Mark thread and step up
e                  — Archive marked threads
d                  — Move marked threads to trash
I                  — Mark marked threads as read
l                  — Label marked threads
L                  — Unlabel marked threads
*                  — Toggle starred on highlighted thread
c                  — Compose new message
C                  — Continue message from draft
N, n, ^N, j, Down  — Next thread
P, p, ^P, k, Up    — Previous thread
r, ^R              — Reload current view
g                  — Go to label
1                  — Go to inbox
U                  — Mark marked threads as unread
s, ^s              — Search
q                  — Quit
^L                 — Refresh screen

Press [enter] to exit
`
)

// ThreadListView is the state for a thread list view.
type ThreadListView struct {
	// Static state.
	label string
	query string

	// Communicate with main thread.
	keys     *input.Input
	errors   chan error
	pageCh   chan *cmdg.ThreadPage
	threadCh chan *cmdg.Thread

	// Only for use by main thread.
	threads []*cmdg.Thread
	pos     int
}

// NewThreadListView creates a new thread list view.
func NewThreadListView(ctx context.Context, label, q string, in *input.Input) *ThreadListView {
	v := &ThreadListView{
		label:    label,
		errors:   make(chan error, 20),
		pageCh:   make(chan *cmdg.ThreadPage),
		threadCh: make(chan *cmdg.Thread),
		keys:     in,
		query:    q,
	}
	go v.fetchPage(ctx, "")
	return v
}

func (tv *ThreadListView) fetchPage(ctx context.Context, token string) {
	listCtx, listCancel := context.WithTimeout(ctx, messageListReloadTimeout)
	defer listCancel()

	log.Infof("Listing threads on label %q query %q with token %q…", tv.label, tv.query, token)
	st := time.Now()
	page, err := conn.ListThreads(listCtx, tv.label, tv.query, token)
	if err != nil {
		tv.errors <- err
		return
	}
	log.Infof("Listing threads took %v", time.Since(st))
	go func() {
		if err := page.PreloadMetadata(ctx, tv.threadCh); err != nil {
			tv.errors <- err
		}
	}()
	tv.pageCh <- page
}

// ThreadViewOp is an operation to perform as the conversation view closes.
type ThreadViewOp struct {
	fun         func(*ThreadListView)
	quit        bool
	nextThread  bool
	prevThread  bool
	next        *ThreadViewOp
}

// Do does the op.
func (op *ThreadViewOp) Do(view *ThreadListView) {
	if op == nil {
		return
	}
	if op.fun != nil {
		op.fun(view)
	}
	if op.next != nil {
		op.next.Do(view)
	}
}

// IsQuit returns if op is `quit`.
func (op *ThreadViewOp) IsQuit() bool {
	if op == nil {
		return false
	}
	if op.quit {
		return true
	}
	if op.next != nil {
		return op.next.IsQuit()
	}
	return false
}

// IsNext returns if op is `next`.
func (op *ThreadViewOp) IsNext() bool {
	if op == nil {
		return false
	}
	if op.nextThread {
		return true
	}
	if op.next != nil {
		return op.next.IsNext()
	}
	return false
}

// IsPrev returns if op is `prev`.
func (op *ThreadViewOp) IsPrev() bool {
	if op == nil {
		return false
	}
	if op.prevThread {
		return true
	}
	if op.next != nil {
		return op.next.IsPrev()
	}
	return false
}

// ThreadOpRemoveCurrent creates an op to remove current thread from the list.
func ThreadOpRemoveCurrent(next *ThreadViewOp) *ThreadViewOp {
	return &ThreadViewOp{
		fun: func(view *ThreadListView) {
			if view.pos < len(view.threads) {
				view.threads = append(view.threads[:view.pos], view.threads[view.pos+1:]...)
				if view.pos >= len(view.threads) && view.pos != 0 {
					view.pos--
				}
			}
		},
		next: next,
	}
}

// ThreadOpQuit creates an op to quit.
func ThreadOpQuit() *ThreadViewOp {
	return &ThreadViewOp{quit: true}
}

// ThreadOpPrev creates an op to go to the previous thread.
func ThreadOpPrev() *ThreadViewOp {
	return &ThreadViewOp{prevThread: true}
}

// ThreadOpNext creates an op to go to the next thread.
func ThreadOpNext() *ThreadViewOp {
	return &ThreadViewOp{nextThread: true}
}

// Run runs the thread list view.
func (tv *ThreadListView) Run(ctx context.Context) error {
	log.Infof("Running ThreadListView")
	theresMore := true
	var contentHeight int
	marked := map[string]bool{}
	var scroll int
	var screen *display.Screen

	initScreen := func() error {
		var err error
		screen, err = display.NewScreen()
		if err != nil {
			return err
		}
		contentHeight = screen.Height - 2
		scroll = 0
		return nil
	}
	if err := initScreen(); err != nil {
		return err
	}
	defer func() {
		screen.Clear()
		screen.Draw()
	}()

	empty := func() {
		screen.Printf(0, 0, "Loading…")
		screen.Draw()
		tv.threads = nil
		tv.pos = 0
		scroll = 0
	}
	empty()

	drawThread := func(cur int) error {
		s := "Loading…"
		if cur >= len(tv.threads) {
			return fmt.Errorf("trying to draw thread %d with len %d", cur, len(tv.threads))
		}
		curThread := tv.threads[cur]

		prefix := " "
		reset := display.Reset
		if cur == tv.pos {
			reset = display.Reverse
			prefix = "*"
		}

		if curThread.HasData(cmdg.LevelMetadata) {
			subj, err := curThread.Subject(ctx)
			if err != nil || subj == "" {
				subj = "(No subject)"
			}
			participants, err := curThread.Participants(ctx)
			if err != nil {
				participants = []string{"?"}
			}
			from := strings.Join(participants, ", ")
			count := curThread.MessageCount()
			countStr := ""
			if count > 1 {
				countStr = fmt.Sprintf(" (%d)", count)
			}
			from = display.FixedWidth(from, 24-len(countStr)) + countStr

			// Get time from last message
			tm := "???"
			lastTime, err := curThread.LastMessageTime(ctx)
			if err == nil && !lastTime.IsZero() {
				now := time.Now()
				if lastTime.Year() == now.Year() && lastTime.YearDay() == now.YearDay() {
					tm = lastTime.Format("15:04")
				} else if now.Sub(lastTime) < 7*24*time.Hour {
					tm = lastTime.Format("Mon02")
				} else {
					tm = lastTime.Format("Jan02")
				}
			}

			s = fmt.Sprintf("%[1]*.[1]*[2]s | %[3]s | %[4]s",
				6, tm,
				from, subj)
			s += reset
		} else {
			go func() {
				if err := curThread.Preload(ctx, cmdg.LevelMetadata); err != nil {
					log.Warningf("Failed to load metadata for thread %s: %v", curThread.ID, err)
				} else {
					tv.threadCh <- curThread
				}
			}()
		}

		if marked[curThread.ID] {
			prefix += "X"
		} else {
			prefix += " "
		}

		if curThread.IsUnread() {
			prefix = display.Bold + prefix + ">"
		} else {
			prefix += " "
		}

		star := " "
		if curThread.IsStarred() {
			star = "*"
			prefix = display.Yellow + prefix
		}

		screen.Printlnf(cur-scroll, "%s%s%s%s", reset, prefix, star, s)
		return nil
	}

	prev := func() bool {
		if tv.pos <= 0 {
			return false
		}
		tv.pos--
		if scroll > 0 && tv.pos < scroll+scrollLimit {
			scroll--
		}
		return true
	}
	next := func() bool {
		if tv.threads == nil {
			return false
		}
		if tv.pos >= len(tv.threads)-1 {
			return false
		}
		if tv.pos-scroll > contentHeight-scrollLimit {
			scroll++
		}
		tv.pos++
		return true
	}

	for {
		status := ""
		select {
		case err := <-tv.errors:
			showError(screen, tv.keys, err.Error())
			screen.Draw()
			continue
		case t := <-tv.threadCh:
			// Thread metadata loaded, redraw its row.
			for i, th := range tv.threads {
				if th.ID == t.ID {
					if err := drawThread(i); err != nil {
						tv.errors <- errors.Wrapf(err, "Drawing thread")
					}
					break
				}
			}
			screen.Draw()
			continue
		case p := <-tv.pageCh:
			log.Printf("ThreadListView: Got page!")
			tv.threads = append(tv.threads, p.Threads...)
			want := contentHeight
			if p.Response.NextPageToken == "" {
				log.Infof("All thread pages loaded")
				if len(tv.threads) == 0 {
					screen.Printlnf(0, "<empty>")
				}
				theresMore = false
			} else {
				if want > len(tv.threads) {
					go tv.fetchPage(ctx, p.Response.NextPageToken)
				} else {
					log.Infof("Enough thread pages. Have %d threads, want %d", len(tv.threads), want)
					theresMore = false
				}
			}

		case key, ok := <-tv.keys.Chan():
			if !ok {
				log.Errorf("ThreadList: Input channel closed!")
				continue
			}
			log.Debugf("ThreadListView got key %q", key)
			switch key {
			case "?", input.F1:
				if err := help(threadListViewHelp, tv.keys); err != nil {
					log.Infof("help() failed: %v", err)
				}
			case input.Enter, input.Right:
				if len(tv.threads) == 0 || tv.pos >= len(tv.threads) {
					break
				}
				for {
					cv := NewConversationView(ctx, tv.threads[tv.pos], tv.keys)
					op, err := cv.Run(ctx)
					if err != nil {
						tv.errors <- errors.Wrapf(err, "Running ConversationView")
						break
					}
					if op != nil {
						op.Do(tv)
					}
					if op.IsQuit() {
						return nil
					}
					if op.IsPrev() {
						if tv.pos > 0 {
							tv.pos--
							if scroll > 0 {
								scroll--
							}
						}
						continue
					}
					if op.IsNext() {
						if tv.pos < len(tv.threads)-1 {
							tv.pos++
							if tv.pos-scroll > contentHeight-scrollLimit {
								scroll++
							}
						}
						continue
					}
					break
				}
			case input.CtrlL:
				if err := initScreen(); err != nil {
					return err
				}
			case "e":
				ids := tv.markedIDs(marked)
				if len(ids) == 0 {
					break
				}
				toRemove := tv.markedThreads(marked)
				go func() {
					for _, t := range toRemove {
						if err := t.RemoveLabelID(ctx, cmdg.Inbox); err != nil {
							tv.errors <- errors.Wrapf(err, "archiving thread")
						}
					}
				}()
				if tv.label == cmdg.Inbox {
					tv.removeMarked(marked, &scroll)
					marked = map[string]bool{}
				}
			case "I": // Mark read.
				threads := tv.markedThreads(marked)
				if len(threads) == 0 {
					break
				}
				go func() {
					for _, t := range threads {
						if err := t.RemoveLabelID(ctx, cmdg.Unread); err != nil {
							tv.errors <- errors.Wrapf(err, "marking thread read")
						}
					}
				}()
			case "U": // Mark unread.
				threads := tv.markedThreads(marked)
				if len(threads) == 0 {
					break
				}
				go func() {
					for _, t := range threads {
						if err := t.AddLabelID(ctx, cmdg.Unread); err != nil {
							tv.errors <- errors.Wrapf(err, "marking thread unread")
						}
					}
				}()
			case "d":
				threads := tv.markedThreads(marked)
				if len(threads) == 0 {
					break
				}
				go func() {
					for _, t := range threads {
						if err := t.Trash(ctx); err != nil {
							tv.errors <- errors.Wrapf(err, "trashing thread")
						}
					}
				}()
				tv.removeMarked(marked, &scroll)
				marked = map[string]bool{}
			case "*":
				if tv.pos >= len(tv.threads) {
					break
				}
				curThread := tv.threads[tv.pos]
				if curThread.IsStarred() {
					go func() {
						if err := curThread.RemoveLabelID(ctx, cmdg.Starred); err != nil {
							tv.errors <- errors.Wrapf(err, "removing STARRED")
						}
					}()
				} else {
					go func() {
						if err := curThread.AddLabelID(ctx, cmdg.Starred); err != nil {
							tv.errors <- errors.Wrapf(err, "adding STARRED")
						}
					}()
				}
			case "l":
				threads := tv.markedThreads(marked)
				if len(threads) == 0 && tv.pos < len(tv.threads) {
					threads = []*cmdg.Thread{tv.threads[tv.pos]}
				}
				if len(threads) == 0 {
					break
				}
				var opts []*dialog.Option
				for _, l := range conn.Labels() {
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.Label,
					})
				}
				label, err := dialog.Selection(opts, "Label> ", false, tv.keys)
				if errors.Cause(err) == dialog.ErrAborted {
					// No-op.
				} else if err != nil {
					tv.errors <- errors.Wrapf(err, "Selecting label")
				} else {
					for _, t := range threads {
						if err := t.AddLabelID(ctx, label.Key); err != nil {
							tv.errors <- errors.Wrapf(err, "labelling thread")
						}
					}
				}
			case "L":
				threads := tv.markedThreads(marked)
				if len(threads) == 0 && tv.pos < len(tv.threads) {
					threads = []*cmdg.Thread{tv.threads[tv.pos]}
				}
				if len(threads) == 0 {
					break
				}
				var opts []*dialog.Option
				for _, l := range conn.Labels() {
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.Label,
					})
				}
				label, err := dialog.Selection(opts, "Label> ", false, tv.keys)
				if errors.Cause(err) == dialog.ErrAborted {
					// No-op.
				} else if err != nil {
					tv.errors <- errors.Wrapf(err, "Selecting label")
				} else {
					for _, t := range threads {
						if err := t.RemoveLabelID(ctx, label.Key); err != nil {
							tv.errors <- errors.Wrapf(err, "unlabelling thread")
						}
					}
				}
			case "c":
				if err := composeNew(ctx, conn, tv.keys); err != nil {
					tv.errors <- errors.Wrapf(err, "Composing new message")
				}
			case "C":
				if err := continueDraft(ctx, conn, tv.keys); err != nil {
					tv.errors <- errors.Wrapf(err, "Continuing draft")
				}
			case input.Home, input.XHome:
				tv.pos = 0
				scroll = 0
			case "x", " ":
				if tv.pos < len(tv.threads) {
					marked[tv.threads[tv.pos].ID] = !marked[tv.threads[tv.pos].ID]
					next()
				}
			case "X":
				if tv.pos < len(tv.threads) {
					marked[tv.threads[tv.pos].ID] = !marked[tv.threads[tv.pos].ID]
					prev()
				}
			case "u":
				marked = map[string]bool{}
			case "N", "n", "j", input.CtrlN, input.Down:
				screen.UseCache()
				if !next() {
					continue
				}
			case "P", "p", "k", input.CtrlP, input.Up:
				screen.UseCache()
				if !prev() {
					continue
				}
			case "r", input.CtrlR:
				empty()
				screen.Clear()
				go tv.fetchPage(ctx, "")
			case "g":
				var opts []*dialog.Option
				for _, l := range conn.Labels() {
					if strings.HasPrefix(l.ID, "CATEGORY_") {
						continue
					}
					if l.ID == "IMPORTANT" {
						continue
					}
					opts = append(opts, &dialog.Option{
						Key:   l.ID,
						Label: l.LabelString(),
					})
				}
				label, err := dialog.Selection(opts, "Label> ", false, tv.keys)
				if errors.Cause(err) == dialog.ErrAborted {
					// No-op.
				} else if err != nil {
					tv.errors <- errors.Wrapf(err, "Selecting label")
				} else {
					nv := NewThreadListView(ctx, label.Key, "", tv.keys)
					return nv.Run(ctx)
				}
			case "1":
				return NewThreadListView(ctx, cmdg.Inbox, "", tv.keys).Run(ctx)
			case "s", input.CtrlS:
				q, err := dialog.Entry("Query> ", tv.keys)
				if err == dialog.ErrAborted {
					// That's fine.
				} else if err != nil {
					tv.errors <- errors.Wrapf(err, "Getting query")
				} else {
					nv := NewThreadListView(ctx, "", q, tv.keys)
					return nv.Run(ctx)
				}
			case "q":
				return nil
			default:
				log.Infof("ThreadListView got unknown key %q %v", key, []byte(key))
			}
		}
		if tv.threads != nil {
			// Draw to buffer.
			st := time.Now()
			for n := 0; n < contentHeight; n++ {
				cur := n + scroll
				if cur >= len(tv.threads) {
					screen.Printlnf(n, "")
					continue
				}
				if err := drawThread(cur); err != nil {
					tv.errors <- err
				}
				if time.Since(st) > 10*time.Millisecond {
					screen.Draw()
				}
			}
			if len(tv.threads) == 0 {
				screen.Printlnf(0, "<empty>")
			}
			log.Debugf("Thread print took %v", time.Since(st))
		}
		// Print status.
		if theresMore {
			status += display.Color(50) + "Loading…"
		}
		screen.Printlnf(screen.Height-2, "%s", strings.Repeat("—", screen.Width))
		screen.Printlnf(screen.Height-1, "%s", status)

		// Draw.
		st := time.Now()
		screen.Draw()
		log.Debugf("Draw took %v", time.Since(st))
	}
}

func (tv *ThreadListView) markedIDs(marked map[string]bool) []string {
	var ids []string
	for id, m := range marked {
		if m {
			ids = append(ids, id)
		}
	}
	return ids
}

func (tv *ThreadListView) markedThreads(marked map[string]bool) []*cmdg.Thread {
	var threads []*cmdg.Thread
	for _, t := range tv.threads {
		if marked[t.ID] {
			threads = append(threads, t)
		}
	}
	return threads
}

func (tv *ThreadListView) removeMarked(marked map[string]bool, scroll *int) {
	var remaining []*cmdg.Thread
	ofs := 0
	for i, t := range tv.threads {
		if marked[t.ID] {
			if i < tv.pos {
				ofs++
			}
		} else {
			remaining = append(remaining, t)
		}
	}
	tv.threads = remaining
	tv.pos -= ofs
	*scroll -= ofs
	if *scroll < 0 {
		*scroll = 0
	}
	if tv.pos >= len(tv.threads) && tv.pos > 0 {
		tv.pos = len(tv.threads) - 1
	}
}
