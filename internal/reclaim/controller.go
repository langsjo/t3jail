package reclaim

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Starting guesses. Reclaim backs off when tasks stalled on memory for more
// than 0.1% of the interval, or when more than 1 MiB per 10 s of evicted file
// pages was read back in. The limits scale with the interval so changing it
// doesn't change how sensitive the backoff is.
const (
	stallFraction        = 1000
	refaultBytesPer10Sec = 1 << 20
	cacheDivisor         = 50
	backoffDuration      = time.Minute
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
	source            source
	floorKiB          uint64
	stallLimitUS      uint64
	refaultLimitPages uint64
	previous          Sample
	havePrevious      bool
	backoffUntil      time.Time
}

func NewController(src source, floorKiB uint64, pageBytes int, interval time.Duration) *Controller {
	refaultBytes := uint64(interval.Milliseconds()) * refaultBytesPer10Sec / 10_000
	return &Controller{
		source:            src,
		floorKiB:          floorKiB,
		stallLimitUS:      uint64(interval.Microseconds()) / stallFraction,
		refaultLimitPages: refaultBytes / uint64(pageBytes),
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

	// Linux 6.18's proactive memcg reclaim leaves the writer outside PSI
	// memstall accounting, so a reclaim write needs no stall correction.
	if grewByMore(current.StallUS, previous.StallUS, c.stallLimitUS) ||
		grewByMore(current.RefaultPages, previous.RefaultPages, c.refaultLimitPages) {
		c.backoffUntil = now.Add(backoffDuration)
	}
	if now.Before(c.backoffUntil) {
		return nil
	}

	step := current.FileCacheKiB / cacheDivisor
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
