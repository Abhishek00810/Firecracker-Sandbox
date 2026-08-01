package worker

import (
	"context"
	"net"
	"testing"
	"time"

	workerv1 "backend/internal/rpc/worker/v1"
	"backend/internal/terminal"

	"github.com/soheilhy/cmux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestTerminalGRPCOpenAndClose(t *testing.T) {
	service := &fakeSessionService{}
	server := NewTerminalGRPCServer(service)

	opened, err := server.OpenTerminal(context.Background(), &workerv1.OpenTerminalRequest{
		TerminalId: "term_123",
		SandboxId:  "sandbox-1",
		Shell:      "/bin/bash",
		Columns:    120,
		Rows:       32,
	})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	if opened.GetTerminalId() != "term_123" || opened.GetState() != "ready" {
		t.Fatalf("unexpected open response: %+v", opened)
	}
	if service.openedTerminal != "term_123" || service.openedSandbox != "sandbox-1" {
		t.Fatalf("service received sandbox=%q terminal=%q", service.openedSandbox, service.openedTerminal)
	}

	closed, err := server.CloseTerminal(context.Background(), &workerv1.CloseTerminalRequest{
		TerminalId: "term_123",
		SandboxId:  "sandbox-1",
	})
	if err != nil {
		t.Fatalf("CloseTerminal: %v", err)
	}
	if closed.GetState() != "closed" || service.closedTerminal != "term_123" {
		t.Fatalf("unexpected close response: %+v", closed)
	}
}

func TestTerminalGRPCSharesWorkerPortWithHTTP(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	multiplexer := cmux.New(listener)
	grpcListener := multiplexer.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(UnaryTokenAuth("worker-secret")),
		grpc.StreamInterceptor(StreamTokenAuth("worker-secret")),
	)
	service := &fakeSessionService{}
	service.attach = func(stream terminal.Stream) error {
		if err := stream.Send(terminal.Frame{Type: terminal.FrameReady}); err != nil {
			return err
		}
		if err := stream.Send(terminal.Frame{Type: terminal.FrameOutput, Data: []byte("guest output")}); err != nil {
			return err
		}
		frame, err := stream.Receive()
		service.attachedInput = frame
		return err
	}
	workerv1.RegisterWorkerTerminalServiceServer(grpcServer, NewTerminalGRPCServer(service))
	go func() { _ = grpcServer.Serve(grpcListener) }()
	go func() { _ = multiplexer.Serve() }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := grpc.NewClient(
		"passthrough:///worker",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx = metadata.AppendToOutgoingContext(ctx, workerTokenMetadata, "worker-secret")
	_, err = workerv1.NewWorkerTerminalServiceClient(connection).OpenTerminal(ctx, &workerv1.OpenTerminalRequest{
		SandboxId: "sandbox-1", TerminalId: "term_shared", Shell: "/bin/bash", Columns: 120, Rows: 32,
	})
	if err != nil {
		t.Fatalf("OpenTerminal over shared worker port: %v", err)
	}
	if service.openedTerminal != "term_shared" {
		t.Fatalf("worker received terminal %q", service.openedTerminal)
	}

	stream, err := workerv1.NewWorkerTerminalServiceClient(connection).AttachTerminal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&workerv1.TerminalFrame{
		Type: workerv1.TerminalFrameType_TERMINAL_FRAME_TYPE_ATTACH, TerminalId: "term_shared", SandboxId: "sandbox-1",
	}); err != nil {
		t.Fatal(err)
	}
	ready, err := stream.Recv()
	if err != nil || ready.GetType() != workerv1.TerminalFrameType_TERMINAL_FRAME_TYPE_READY {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	output, err := stream.Recv()
	if err != nil || string(output.GetData()) != "guest output" {
		t.Fatalf("output=%v err=%v", output, err)
	}
	if err := stream.Send(&workerv1.TerminalFrame{
		Type: workerv1.TerminalFrameType_TERMINAL_FRAME_TYPE_INPUT, Data: []byte("pwd\n"),
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = stream.Recv()
	if service.attachedInput.Type != terminal.FrameInput || string(service.attachedInput.Data) != "pwd\n" {
		t.Fatalf("unexpected attached input: %+v", service.attachedInput)
	}
}

func TestTerminalGRPCRejectsInvalidOptions(t *testing.T) {
	server := NewTerminalGRPCServer(&fakeSessionService{})
	_, err := server.OpenTerminal(context.Background(), &workerv1.OpenTerminalRequest{
		TerminalId: "term_123", SandboxId: "sandbox-1", Shell: "/bin/sh", Columns: 120, Rows: 32,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s want InvalidArgument: %v", status.Code(err), err)
	}
}

func TestUnaryTokenAuth(t *testing.T) {
	interceptor := UnaryTokenAuth("worker-secret")
	handler := func(context.Context, any) (any, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: workerv1.WorkerTerminalService_OpenTerminal_FullMethodName}

	if _, err := interceptor(context.Background(), nil, info, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token code=%s want Unauthenticated", status.Code(err))
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(workerTokenMetadata, "worker-secret"))
	response, err := interceptor(ctx, nil, info, handler)
	if err != nil || response != "ok" {
		t.Fatalf("valid token response=%v err=%v", response, err)
	}
}
