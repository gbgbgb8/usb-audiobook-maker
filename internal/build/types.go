package build

import (
	"context"
	"time"

	"audiobooks/internal/device"
	"audiobooks/internal/library"
)

type Mode int

const (
	ModeLibrary Mode = 1
	ModeDaisy   Mode = 7
)

func (m Mode) String() string {
	if m == ModeLibrary {
		return "Library Mode"
	}
	return "DAISY 2.02"
}

type Request struct {
	Book       library.Book
	Device     device.Device
	Mode       Mode
	Metadata   library.Metadata
	Format     bool
	DryRun     bool
	Repository string
}

type Event struct {
	Phase   string
	Message string
	Percent float64
	Done    bool
	Err     error
	At      time.Time
}

type Service interface {
	Run(context.Context, Request) <-chan Event
}
