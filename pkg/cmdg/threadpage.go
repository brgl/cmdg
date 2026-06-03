package cmdg

import (
	"context"

	gmail "google.golang.org/api/gmail/v1"
)

// ThreadPage holds a page of thread list results.
type ThreadPage struct {
	Label string
	Query string

	conn     *CmdG
	Threads  []*Thread
	Response *gmail.ListThreadsResponse
}

// Next returns the next page of threads.
func (p *ThreadPage) Next(ctx context.Context) (*ThreadPage, error) {
	return p.conn.ListThreads(ctx, p.Label, p.Query, p.Response.NextPageToken)
}

// PreloadMetadata async loads thread metadata for all threads on this page.
// If notify is non-nil, each successfully loaded thread is sent to it.
func (p *ThreadPage) PreloadMetadata(ctx context.Context, notify chan<- *Thread) error {
	conc := 10
	sem := make(chan struct{}, conc)
	num := len(p.Threads)
	errs := make([]error, num)
	for n := 0; n < num; n++ {
		n := n
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			if err := p.Threads[n].Preload(ctx, LevelMetadata); err != nil {
				errs[n] = err
			} else if notify != nil {
				notify <- p.Threads[n]
			}
		}()
	}
	for t := 0; t < conc; t++ {
		sem <- struct{}{}
	}
	return nil
}
