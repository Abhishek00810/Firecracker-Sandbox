package firecracker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"backend/internal/terminal"
)

const (
	terminalWireInput  byte = 1
	terminalWireOutput byte = 2
	terminalWireResize byte = 3
	terminalWireExit   byte = 4
	terminalWireError  byte = 5
	terminalWireClose  byte = 6

	maxTerminalWirePayload = 1 << 20
)

type terminalStreamResult struct {
	frame terminal.Frame
	err   error
}

func (v *VsockClient) AttachTerminal(ctx context.Context, terminalID string, stream terminal.Stream) error {
	conn, err := v.Connect()
	if err != nil {
		return fmt.Errorf("connect terminal stream: %w", err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(struct {
		Type       string `json:"type"`
		TerminalID string `json:"terminal_id"`
	}{Type: "terminal_attach", TerminalID: terminalID}); err != nil {
		return fmt.Errorf("send terminal attach: %w", err)
	}
	reader := bufio.NewReader(conn)
	responseLine, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read terminal attach response: %w", err)
	}
	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(responseLine, &response); err != nil {
		return fmt.Errorf("decode terminal attach response: %w", err)
	}
	if !response.Success {
		return fmt.Errorf("terminal attach failed: %s", response.Error)
	}
	if err := stream.Send(terminal.Frame{Type: terminal.FrameReady}); err != nil {
		return err
	}

	guestFrames := make(chan terminalStreamResult, 1)
	clientFrames := make(chan terminalStreamResult, 1)
	go receiveTerminalWire(reader, guestFrames)
	go receiveTerminalClient(stream, clientFrames)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-guestFrames:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				return result.err
			}
			if err := stream.Send(result.frame); err != nil {
				return err
			}
			if result.frame.Type == terminal.FrameExit || result.frame.Type == terminal.FrameError {
				return nil
			}
		case result := <-clientFrames:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				return result.err
			}
			switch result.frame.Type {
			case terminal.FrameInput:
				if len(result.frame.Data) > 64*1024 {
					return errors.New("terminal input frame exceeds 64 KiB")
				}
				if err := writeTerminalWire(conn, terminalWireInput, result.frame.Data); err != nil {
					return err
				}
			case terminal.FrameResize:
				if result.frame.Columns < 20 || result.frame.Columns > 500 || result.frame.Rows < 5 || result.frame.Rows > 200 {
					return errors.New("terminal resize is outside the supported range")
				}
				payload := make([]byte, 4)
				binary.BigEndian.PutUint16(payload[0:2], uint16(result.frame.Columns))
				binary.BigEndian.PutUint16(payload[2:4], uint16(result.frame.Rows))
				if err := writeTerminalWire(conn, terminalWireResize, payload); err != nil {
					return err
				}
			case terminal.FrameClose:
				_ = writeTerminalWire(conn, terminalWireClose, nil)
				return nil
			default:
				return fmt.Errorf("unsupported terminal client frame %q", result.frame.Type)
			}
		}
	}
}

func receiveTerminalClient(stream terminal.Stream, results chan<- terminalStreamResult) {
	for {
		frame, err := stream.Receive()
		results <- terminalStreamResult{frame: frame, err: err}
		if err != nil {
			return
		}
	}
}

func receiveTerminalWire(reader io.Reader, results chan<- terminalStreamResult) {
	for {
		wireType, payload, err := readTerminalWire(reader)
		if err != nil {
			results <- terminalStreamResult{err: err}
			return
		}
		frame := terminal.Frame{Data: payload}
		switch wireType {
		case terminalWireOutput:
			frame.Type = terminal.FrameOutput
		case terminalWireExit:
			if len(payload) != 4 {
				results <- terminalStreamResult{err: errors.New("invalid terminal exit frame")}
				return
			}
			frame.Type = terminal.FrameExit
			frame.ExitCode = int32(binary.BigEndian.Uint32(payload))
			frame.Data = nil
		case terminalWireError:
			frame.Type = terminal.FrameError
			frame.Message = string(payload)
			frame.Data = nil
		default:
			results <- terminalStreamResult{err: fmt.Errorf("unsupported guest terminal frame %d", wireType)}
			return
		}
		results <- terminalStreamResult{frame: frame}
		if frame.Type == terminal.FrameExit || frame.Type == terminal.FrameError {
			return
		}
	}
}

func writeTerminalWire(writer io.Writer, frameType byte, payload []byte) error {
	if len(payload) > maxTerminalWirePayload {
		return errors.New("terminal frame exceeds maximum payload")
	}
	header := [5]byte{frameType}
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}

func readTerminalWire(reader io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if size > maxTerminalWirePayload {
		return 0, nil, errors.New("terminal frame exceeds maximum payload")
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}
