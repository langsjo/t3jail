package reclaim

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Starting guesses. Each sample reclaims 2% of the file cache. Reclaim backs
// off when tasks stalled on memory for more than 0.1% of the interval, or when
// evicted file pages worth more than a tenth of a step were read back in:
// either means reclaim is cutting into the working set. The refault limit
// follows the step, so it is lenient while there is a lot of cache and
// careful when little remains.
const (
	stallFraction   = 1000
	cacheDivisor    = 50
	refaultDivisor  = 10
	backoffDuration = time.Minute
)

type Sample struct {
	StallUS      uint64
	RefaultPages uint64
	FileCacheKiB uint64
}

type source interface {
	Sample() (Sample, error)
	Reclaim(kib uint64) error
}

type Controller struct {
	source       source
	floorKiB     uint64
	pageKiB      uint64
	stallLimitUS uint64
	previous     Sample
	havePrevious bool
	backoffUntil time.Time
}

func NewController(src source, floorKiB uint64, pageBytes int, interval time.Duration) *Controller {
	return &Controller{
		source:       src,
		floorKiB:     floorKiB,
		pageKiB:      uint64(pageBytes) / 1024,
		stallLimitUS: uint64(interval.Microseconds()) / stallFraction,
	}
}

func (c *Controller) Tick(now time.Time) error {
	current, err := c.source.Sample()
	if err != nil {
		c.havePrevious = false
		return err
	}

	previous, havePrevious := c.previous, c.havePrevious
	c.previous, c.havePrevious = current, true
	if !havePrevious {
		return nil
	}

	step := current.FileCacheKiB / cacheDivisor
	refaultLimitPages := step / refaultDivisor / c.pageKiB
	// Linux 6.18's proactive memcg reclaim leaves the writer outside PSI
	// memstall accounting, so a reclaim write needs no stall correction.
	if grewByMore(current.StallUS, previous.StallUS, c.stallLimitUS) ||
		grewByMore(current.RefaultPages, previous.RefaultPages, refaultLimitPages) {
		c.backoffUntil = now.Add(backoffDuration)
	}
	if now.Before(c.backoffUntil) {
		return nil
	}

	if step == 0 || current.FileCacheKiB-step <= c.floorKiB {
		return nil
	}
	err = c.source.Reclaim(step)
	if errors.Is(err, syscall.EAGAIN) {
		// The rest is hard to reclaim. Retrying every sample would keep the
		// kernel scanning down to its highest priority and shrinking slab.
		c.backoffUntil = now.Add(backoffDuration)
		return nil
	}
	if err != nil {
		return fmt.Errorf("reclaim %d KiB: %w", step, err)
	}
	return nil
}

func grewByMore(current, previous, limit uint64) bool {
	return current > previous && current-previous > limit
}
