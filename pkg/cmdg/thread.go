package cmdg

import (
	"context"
	"net/mail"
	"sync"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	gmail "google.golang.org/api/gmail/v1"
)

// Thread represents a Gmail thread (conversation).
type Thread struct {
	m    sync.RWMutex
	conn *CmdG

	level    DataLevel
	ID       string
	Messages []*Message
	Response *gmail.Thread
}

// NewThreadObj creates a new thread with just an ID.
func NewThreadObj(c *CmdG, threadID string) *Thread {
	return c.ThreadCache(&Thread{
		conn: c,
		ID:   threadID,
	})
}

// NewThreadWithResponse creates a thread from data already received from the Gmail API.
func NewThreadWithResponse(c *CmdG, threadID string, resp *gmail.Thread, level DataLevel) *Thread {
	t := NewThreadObj(c, threadID)
	t.m.Lock()
	defer t.m.Unlock()
	if !hasData(t.level, level) {
		t.Response = resp
		t.level = level
		if resp.Messages != nil {
			t.Messages = make([]*Message, 0, len(resp.Messages))
			for _, m := range resp.Messages {
				t.Messages = append(t.Messages, NewMessageWithResponse(c, m.Id, m, level))
			}
		}
	}
	return t
}

// HasData returns whether the thread has at least the given level of data.
func (t *Thread) HasData(level DataLevel) bool {
	t.m.RLock()
	defer t.m.RUnlock()
	return hasData(t.level, level)
}

// Preload loads thread data to at least the given level.
func (t *Thread) Preload(ctx context.Context, level DataLevel) error {
	if t.HasData(level) {
		return nil
	}
	return t.load(ctx, level)
}

func (t *Thread) load(ctx context.Context, level DataLevel) error {
	st := time.Now()
	log.Debugf("Loading thread %q at level %v", t.ID, level)
	var resp *gmail.Thread
	err := wrapLogRPC("gmail.Users.Threads.Get", func() (err error) {
		resp, err = t.conn.gmail.Users.Threads.Get(email, t.ID).
			Format(string(level)).
			Context(ctx).
			Do()
		return
	}, "email=%q threadID=%v level=%s", email, t.ID, level)
	if err != nil {
		return errors.Wrapf(err, "loading thread %q", t.ID)
	}
	log.Debugf("Downloading thread %q level %q took %v", t.ID, level, time.Since(st))

	t.m.Lock()
	defer t.m.Unlock()
	t.Response = resp
	t.level = level
	t.Messages = make([]*Message, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		t.Messages = append(t.Messages, NewMessageWithResponse(t.conn, m.Id, m, level))
	}
	return nil
}

// Snippet returns the thread snippet from the list response.
func (t *Thread) Snippet() string {
	t.m.RLock()
	defer t.m.RUnlock()
	if t.Response == nil {
		return ""
	}
	return t.Response.Snippet
}

// MessageCount returns the number of messages in the thread.
func (t *Thread) MessageCount() int {
	t.m.RLock()
	defer t.m.RUnlock()
	return len(t.Messages)
}

// Subject returns the subject of the thread (from the first message).
func (t *Thread) Subject(ctx context.Context) (string, error) {
	if err := t.Preload(ctx, LevelMetadata); err != nil {
		return "", err
	}
	t.m.RLock()
	defer t.m.RUnlock()
	if len(t.Messages) == 0 {
		return "", nil
	}
	return t.Messages[0].GetSubject(ctx)
}

// LastMessageTime returns the time of the most recent message in the thread.
func (t *Thread) LastMessageTime(ctx context.Context) (time.Time, error) {
	if err := t.Preload(ctx, LevelMinimal); err != nil {
		return time.Time{}, err
	}
	t.m.RLock()
	defer t.m.RUnlock()
	if t.Response == nil {
		return time.Time{}, nil
	}
	// HistoryId-based ordering: use InternalDate from the last message if available.
	if len(t.Messages) > 0 {
		last := t.Messages[len(t.Messages)-1]
		if last.Response != nil && last.Response.InternalDate > 0 {
			return time.UnixMilli(last.Response.InternalDate), nil
		}
	}
	return time.Time{}, nil
}

// Participants returns a deduplicated list of sender names in the thread.
func (t *Thread) Participants(ctx context.Context) ([]string, error) {
	if err := t.Preload(ctx, LevelMetadata); err != nil {
		return nil, err
	}
	t.m.RLock()
	defer t.m.RUnlock()
	seen := make(map[string]bool)
	var participants []string
	for _, msg := range t.Messages {
		from, err := msg.GetHeader(ctx, "From")
		if err != nil {
			continue
		}
		name := from
		if a, err := mail.ParseAddress(from); err == nil {
			if a.Name != "" {
				name = a.Name
			} else {
				name = a.Address
			}
		}
		if !seen[name] {
			seen[name] = true
			participants = append(participants, name)
		}
	}
	return participants, nil
}

// IsUnread returns true if any message in the thread has the UNREAD label.
func (t *Thread) IsUnread() bool {
	t.m.RLock()
	defer t.m.RUnlock()
	for _, msg := range t.Messages {
		if msg.HasLabel(Unread) {
			return true
		}
	}
	return false
}

// IsStarred returns true if any message in the thread has the STARRED label.
func (t *Thread) IsStarred() bool {
	t.m.RLock()
	defer t.m.RUnlock()
	for _, msg := range t.Messages {
		if msg.HasLabel(Starred) {
			return true
		}
	}
	return false
}

// HasLabel checks if any message in the thread has the given label.
func (t *Thread) HasLabel(labelID string) bool {
	t.m.RLock()
	defer t.m.RUnlock()
	for _, msg := range t.Messages {
		if msg.HasLabel(labelID) {
			return true
		}
	}
	return false
}

// LabelIds returns the union of all label IDs across messages in the thread.
func (t *Thread) LabelIds() []string {
	t.m.RLock()
	defer t.m.RUnlock()
	seen := make(map[string]bool)
	var labels []string
	for _, msg := range t.Messages {
		for _, l := range msg.LocalLabels() {
			if !seen[l] {
				seen[l] = true
				labels = append(labels, l)
			}
		}
	}
	return labels
}

// AddLabelID adds a label to the entire thread.
func (t *Thread) AddLabelID(ctx context.Context, labelID string) error {
	if err := t.conn.ThreadModify(ctx, t.ID, []string{labelID}, nil); err != nil {
		return err
	}
	t.m.Lock()
	defer t.m.Unlock()
	for _, msg := range t.Messages {
		msg.AddLabelIDLocal(labelID)
	}
	return nil
}

// RemoveLabelID removes a label from the entire thread.
func (t *Thread) RemoveLabelID(ctx context.Context, labelID string) error {
	if err := t.conn.ThreadModify(ctx, t.ID, nil, []string{labelID}); err != nil {
		return err
	}
	t.m.Lock()
	defer t.m.Unlock()
	for _, msg := range t.Messages {
		msg.RemoveLabelIDLocal(labelID)
	}
	return nil
}

// Trash trashes the entire thread.
func (t *Thread) Trash(ctx context.Context) error {
	return t.conn.ThreadTrash(ctx, t.ID)
}
