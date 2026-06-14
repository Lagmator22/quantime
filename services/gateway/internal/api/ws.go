// WebSocket handler for live run telemetry.
//
// On connect, we subscribe to the run's Redis pubsub channel and fan
// messages out to the client. We also write keepalive pings every 20s
// so intermediate proxies don't idle the connection.
package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"nhooyr.io/websocket"
)

func (d *Deps) streamRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Caddy already enforces origin
	})
	if err != nil {
		log.Printf("[ws] accept: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ch, unsub, err := d.Cache.SubscribeRun(ctx, runID)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "subscribe failed")
		return
	}
	defer unsub()

	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	// nhooyr.websocket requires a concurrent reader to process control frames (like PONGs).
	// Without this, conn.Ping() will block and time out after 5s, killing the connection.
	go func() {
		for {
			_, _, err := conn.Read(ctx)
			if err != nil {
				cancel()
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			writeCtx, c := context.WithTimeout(ctx, 5*time.Second)
			if err := conn.Write(writeCtx, websocket.MessageText, msg); err != nil {
				c()
				return
			}
			c()
		case <-pingTicker.C:
			pingCtx, c := context.WithTimeout(ctx, 5*time.Second)
			if err := conn.Ping(pingCtx); err != nil {
				c()
				return
			}
			c()
		}
	}
}
