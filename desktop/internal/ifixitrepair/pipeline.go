package repair

import (
	"context"
	"fmt"
	"sync"
)

type pageTask struct {
	en   entry
	data []byte
}
type pageResult struct {
	en     entry
	data   []byte
	links  int
	notice bool
	err    error
}

// One producer owns the archive decoder, workers own only their bounded page
// buffers, and one consumer owns all output writes and directory updates.
func repairPages(ctx context.Context, a *archive, indexes []int, r *resolver, workers int, consume func(pageResult) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan pageTask)
	results := make(chan pageResult, workers)
	var wg sync.WaitGroup
	send := func(result pageResult) bool {
		select {
		case results <- result:
			return true
		case <-ctx.Done():
			return false
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobs)
		for _, i := range indexes {
			if ctx.Err() != nil {
				return
			}
			en := a.entries[i]
			data, err := a.content(en)
			if err != nil {
				send(pageResult{err: fmt.Errorf("%s: %w", en.path, err)})
				return
			}
			task := pageTask{en, append([]byte(nil), data...)}
			select {
			case jobs <- task:
			case <-ctx.Done():
				return
			}
		}
	}()
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case task, ok := <-jobs:
					if !ok {
						return
					}
					data, links, notice, err := repairHTML(task.en.path, task.data, r)
					if !send(pageResult{task.en, data, links, notice, err}) {
						return
					}
					if err != nil {
						return
					}
				}
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()
	var first error
	for result := range results {
		if first != nil {
			continue
		}
		if result.err != nil {
			first = result.err
			cancel()
			continue
		}
		if err := consume(result); err != nil {
			first = err
			cancel()
		}
	}
	if first != nil {
		return first
	}
	return ctx.Err()
}
