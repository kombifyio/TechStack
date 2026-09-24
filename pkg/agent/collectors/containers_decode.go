package collectors

import (
	"encoding/json"
	"io"

	"github.com/moby/moby/api/types/container"
)

func decodeContainerStats(r io.Reader, st *container.StatsResponse) error {
	return json.NewDecoder(r).Decode(st)
}
