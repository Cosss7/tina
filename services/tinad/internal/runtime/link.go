package runtime

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/coder/websocket"
)

func (r *Runtime) Run(ctx context.Context) error {
	u, err := url.Parse(r.config.URL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || r.config.Token == "" {
		return fmt.Errorf("a ws/wss control-plane URL and enrollment token are required")
	}
	for ctx.Err() == nil {
		if err := r.connect(ctx); err != nil && ctx.Err() == nil {
			log.Printf("control-plane link disconnected: %v", err)
		}
		timer := time.NewTimer(300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
func (r *Runtime) connect(lifetime context.Context) error {
	dialCtx, cancel := context.WithTimeout(lifetime, 5*time.Second)
	conn, _, err := websocket.Dial(dialCtx, r.config.URL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + r.config.Token}, "X-Tina-Node": []string{r.config.NodeID}}})
	cancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)
	ctx, cancel := context.WithCancel(lifetime)
	defer cancel()
	write := func(frame []byte) error {
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return conn.Write(callCtx, websocket.MessageText, frame)
	}
	r.mu.Lock()
	registration, err := p.Encode(newID(), r.registration)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err := write(registration); err != nil {
		return err
	}
	errors := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				errors <- err
				return
			}
			frame, err := p.Decode(data)
			if err != nil {
				errors <- err
				return
			}
			if err := r.Handle(lifetime, frame.Message); err != nil {
				errors <- fmt.Errorf("command rejected: %w", err)
				return
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		frames, err := r.journal.pending()
		if err != nil {
			return err
		}
		for _, frame := range frames {
			if err := write(frame); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errors:
			return err
		case <-r.wake:
		case <-r.reconnect:
			return nil
		case <-ticker.C:
			heartbeat, _ := p.Encode(newID(), p.Heartbeat{NodeID: r.config.NodeID})
			if err := write(heartbeat); err != nil {
				return err
			}
		}
	}
}
