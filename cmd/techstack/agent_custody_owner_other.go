//go:build !linux

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
)

// enrollAsCustodyOwner and handoffAsCustodyOwner are Linux-only: the
// privilege-split layout exists only there.
func enrollAsCustodyOwner(context.Context, *flag.FlagSet, string, string, bool, []byte, io.Writer) (bool, error) {
	return false, nil
}

func handoffAsCustodyOwner(context.Context, *flag.FlagSet, string, string, handoffTarget, []byte, io.Writer) (bool, error) {
	return false, nil
}

func inheritedHandoffArchive(int, string) (*os.File, error) {
	return nil, errors.New("--archive-fd is only for the daemon-user child of a privilege-split Guard")
}
