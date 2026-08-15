package collectors

import (
	"encoding/json"
	"io"

	"github.com/docker/docker/api/types/container"
)

func decodeContainerStats(r io.Reader, st *container.StatsResponse) error {
	return json.NewDecoder(r).Decode(st)
}
