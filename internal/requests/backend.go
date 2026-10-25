package requests

import (
	"context"

	"cineroute/internal/torrentmeta"
)

type Existing struct {
	Quality  string
	Score    int
	Conforms bool
}
type Inventory struct {
	Movie    *Existing
	Episodes map[string]Existing
}
type Backend interface {
	Inventory(context.Context, Job, []Download) (Inventory, error)
	Submit(context.Context, Job, Candidate, *torrentmeta.MetaInfo, []byte, []string, func(Download) error) (Download, error)
	Observe(context.Context, Download) (Download, error)
	Pause(context.Context, string) error
}
