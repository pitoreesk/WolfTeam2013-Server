package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
)

// ServeTCP accepts connections until the context is canceled and waits for
// every connection handler before returning.
func ServeTCP(ctx context.Context, address, service string, onListen func(net.Listener), handle func(net.Conn)) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen for %s connections: %w", service, err)
	}
	defer listener.Close()
	if onListen != nil {
		onListen(listener)
	}
	stopClose := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopClose()

	var connections sync.WaitGroup
	defer connections.Wait()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept %s connection: %w", service, err)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			handle(connection)
		}()
	}
}
