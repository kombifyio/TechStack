//go:build !linux

package main

import (
	"context"
	"errors"
)

func bootstrapExistingHostedRIL(context.Context) error {
	return errors.New("RIL host bootstrap requires Linux")
}
