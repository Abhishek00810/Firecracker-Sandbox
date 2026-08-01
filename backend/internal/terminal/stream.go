package terminal

type FrameType string

const (
	FrameReady  FrameType = "ready"
	FrameInput  FrameType = "input"
	FrameOutput FrameType = "output"
	FrameResize FrameType = "resize"
	FrameExit   FrameType = "exit"
	FrameError  FrameType = "error"
	FrameClose  FrameType = "close"
)

type Frame struct {
	Type     FrameType
	Data     []byte
	Columns  uint32
	Rows     uint32
	ExitCode int32
	Message  string
}

type Stream interface {
	Send(Frame) error
	Receive() (Frame, error)
}
