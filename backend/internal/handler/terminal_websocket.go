package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"backend/internal/middleware"
	"backend/internal/terminal"

	"github.com/coder/websocket"
)

type TerminalAttachmentWorker interface {
	AttachTerminal(context.Context, string, string, string) (terminal.Stream, error)
	CloseTerminal(context.Context, string, string, string) error
}

type terminalWebSocketEvent struct {
	Type     string `json:"type"`
	Columns  uint32 `json:"columns,omitempty"`
	Rows     uint32 `json:"rows,omitempty"`
	ExitCode int32  `json:"exit_code,omitempty"`
	Message  string `json:"message,omitempty"`
}

type terminalStreamResult struct {
	frame terminal.Frame
	err   error
}

func AttachTerminalHandler(placements TerminalPlacementResolver, workers TerminalAttachmentWorker, terminals *terminal.Manager, allowedOrigins []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := middleware.RequestIDFromContext(r.Context())
		terminalID := r.PathValue("terminalID")
		session, err := terminals.Consume(r.URL.Query().Get("token"))
		if err != nil || session.ID != terminalID {
			writeTerminalError(w, http.StatusUnauthorized, "invalid_terminal_token", "terminal attachment token is invalid or expired", requestID)
			return
		}

		placement, err := placements.Placement(r.Context(), session.SandboxID)
		if err != nil || placement.WorkerID != session.WorkerID {
			terminals.Cancel(terminalID)
			writeTerminalError(w, http.StatusServiceUnavailable, "terminal_placement_unavailable", "terminal worker placement is unavailable", requestID)
			return
		}

		streamCtx, cancelStream := context.WithCancel(r.Context())
		startupTimer := time.AfterFunc(15*time.Second, cancelStream)
		stream, err := workers.AttachTerminal(streamCtx, placement.Endpoint, session.SandboxID, terminalID)
		if !startupTimer.Stop() && err == nil {
			err = context.DeadlineExceeded
		}
		if err == nil && streamCtx.Err() != nil {
			err = streamCtx.Err()
		}
		if err != nil {
			cancelStream()
			terminals.Cancel(terminalID)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = workers.CloseTerminal(cleanupCtx, placement.Endpoint, session.SandboxID, terminalID)
			cleanupCancel()
			writeTerminalError(w, http.StatusBadGateway, "terminal_attachment_failed", "worker failed to attach terminal", requestID)
			return
		}
		defer cancelStream()
		defer terminals.Cancel(terminalID)
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = workers.CloseTerminal(ctx, placement.Endpoint, session.SandboxID, terminalID)
		}()

		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns:  allowedOrigins,
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		connection.SetReadLimit(64 * 1024)
		defer connection.Close(websocket.StatusNormalClosure, "terminal closed")

		if err := writeTerminalWebSocketEvent(r.Context(), connection, terminalWebSocketEvent{Type: "ready"}); err != nil {
			return
		}
		bridgeTerminalWebSocket(r.Context(), connection, stream)
	}
}

func bridgeTerminalWebSocket(ctx context.Context, connection *websocket.Conn, stream terminal.Stream) {
	workerFrames := make(chan terminalStreamResult, 1)
	webSocketFrames := make(chan terminalStreamResult, 1)
	go receiveWorkerTerminalFrames(stream, workerFrames)
	go receiveWebSocketTerminalFrames(ctx, connection, stream, webSocketFrames)

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := connection.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		case result := <-webSocketFrames:
			if result.err != nil {
				return
			}
			if result.frame.Type == terminal.FrameClose {
				return
			}
		case result := <-workerFrames:
			if result.err != nil {
				return
			}
			switch result.frame.Type {
			case terminal.FrameOutput:
				if err := connection.Write(ctx, websocket.MessageBinary, result.frame.Data); err != nil {
					return
				}
			case terminal.FrameExit:
				_ = writeTerminalWebSocketEvent(ctx, connection, terminalWebSocketEvent{Type: "exit", ExitCode: result.frame.ExitCode})
				return
			case terminal.FrameError:
				_ = writeTerminalWebSocketEvent(ctx, connection, terminalWebSocketEvent{Type: "error", Message: result.frame.Message})
				return
			}
		}
	}
}

func receiveWorkerTerminalFrames(stream terminal.Stream, results chan<- terminalStreamResult) {
	for {
		frame, err := stream.Receive()
		results <- terminalStreamResult{frame: frame, err: err}
		if err != nil || frame.Type == terminal.FrameExit || frame.Type == terminal.FrameError {
			return
		}
	}
}

func receiveWebSocketTerminalFrames(ctx context.Context, connection *websocket.Conn, stream terminal.Stream, results chan<- terminalStreamResult) {
	for {
		messageType, payload, err := connection.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				err = io.EOF
			}
			results <- terminalStreamResult{err: err}
			return
		}

		var frame terminal.Frame
		switch messageType {
		case websocket.MessageBinary:
			frame = terminal.Frame{Type: terminal.FrameInput, Data: payload}
		case websocket.MessageText:
			var event terminalWebSocketEvent
			if err := json.Unmarshal(payload, &event); err != nil {
				results <- terminalStreamResult{err: errors.New("invalid terminal control message")}
				return
			}
			switch event.Type {
			case "resize":
				frame = terminal.Frame{Type: terminal.FrameResize, Columns: event.Columns, Rows: event.Rows}
			case "close":
				frame = terminal.Frame{Type: terminal.FrameClose}
			default:
				results <- terminalStreamResult{err: fmt.Errorf("unsupported terminal control message %q", event.Type)}
				return
			}
		default:
			results <- terminalStreamResult{err: errors.New("unsupported WebSocket message type")}
			return
		}
		if err := stream.Send(frame); err != nil {
			results <- terminalStreamResult{err: err}
			return
		}
		results <- terminalStreamResult{frame: frame}
		if frame.Type == terminal.FrameClose {
			return
		}
	}
}

func writeTerminalWebSocketEvent(ctx context.Context, connection *websocket.Conn, event terminalWebSocketEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return connection.Write(ctx, websocket.MessageText, payload)
}
