package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/grandcat/zeroconf"
)

// ListenMDNS registers a browser for common mDNS service types (workstation/ssh/_http) and
// invokes onEntry for each discovered ServiceEntry. It returns when ctx is done or an error occurs
// starting the resolver. The caller is responsible for canceling ctx when done.
func ListenMDNS(ctx context.Context, onEntry func(*zeroconf.ServiceEntry)) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return fmt.Errorf("mdns resolver: %w", err)
	}

	// Aggregate mDNS service entries from multiple Browse calls into one consumer.
	// Keep channels buffered and drop on overflow to avoid local amplification / deadlocks.
	agg := make(chan *zeroconf.ServiceEntry, 128)
	go func() {
		for {
			select {
			case e := <-agg:
				if e != nil {
					onEntry(e)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Browse several common mDNS service types. Each Browse uses its own entries
	// channel; we forward into a bounded aggregator.
	types := []string{"_workstation._tcp", "_http._tcp", "_ssh._tcp", "_ipp._tcp"}
	for _, t := range types {
		temp := make(chan *zeroconf.ServiceEntry, 16)
		go func(svc string, ch chan *zeroconf.ServiceEntry) {
			_ = resolver.Browse(ctx, svc, "local.", ch)
		}(t, temp)

		go func(ch chan *zeroconf.ServiceEntry) {
			for {
				select {
				case e := <-ch:
					if e == nil {
						continue
					}
					select {
					case agg <- e:
					default:
						// drop if consumer is behind
					}
				case <-ctx.Done():
					return
				}
			}
		}(temp)
	}

	// Allow a short warm-up period for the resolver; the caller controls the
	// overall timeout for discovery via ctx.
	select {
	case <-time.After(10 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
