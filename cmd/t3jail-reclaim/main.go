package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/langsjo/t3jail/internal/reclaim"
)

func main() {
	log.SetFlags(0)
	interval := flag.Duration("interval", 10*time.Second, "time between samples")
	floorMiB := flag.Uint64("keep-cache-mib", 256, "file cache floor in MiB")
	flag.Parse()

	if err := run(*interval, *floorMiB); err != nil {
		log.Fatal(err)
	}
}

func run(interval time.Duration, floorMiB uint64) error {
	if interval <= 0 || floorMiB > math.MaxUint64/1024 {
		return fmt.Errorf("invalid interval %s or file cache floor %d MiB", interval, floorMiB)
	}
	if _, err := os.Stat(reclaim.ReclaimPath); err != nil {
		return fmt.Errorf("memory reclaim interface unavailable: %w", err)
	}
	enabled, err := reclaim.ReportingEnabled()
	if err != nil {
		return fmt.Errorf("check virtio balloon free page reporting: %w", err)
	}
	if !enabled {
		log.Print("virtio balloon free page reporting is unavailable, skipping cache reclaim")
		return nil
	}

	log.Printf("virtio balloon free page reporting enabled, sampling every %s with a %d MiB file cache floor", interval, floorMiB)
	controller := reclaim.NewController(reclaim.Linux{}, floorMiB*1024, os.Getpagesize(), interval)
	for {
		if err := controller.Tick(time.Now()); err != nil {
			log.Print(err)
		}
		// Sleep after each tick rather than on a fixed schedule, so even a
		// slow reclaim write is followed by a full interval of observation.
		time.Sleep(interval)
	}
}
