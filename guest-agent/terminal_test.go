package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestByteRingKeepsNewestBytes(t *testing.T) {
	ring := newByteRing(5)
	_, _ = ring.Write([]byte("abc"))
	_, _ = ring.Write([]byte("def"))
	if !bytes.Equal(ring.data, []byte("bcdef")) {
		t.Fatalf("ring=%q want bcdef", ring.data)
	}
	_, _ = ring.Write([]byte("123456"))
	if !bytes.Equal(ring.data, []byte("23456")) {
		t.Fatalf("ring=%q want 23456", ring.data)
	}
}

func TestValidateTerminalOpen(t *testing.T) {
	valid := terminalOpenRequest{ID: "term_abc-123", Hostname: "1f6552e4-cf25-42b1-929b-7fd35a086f1b", Shell: "/bin/bash", Columns: 120, Rows: 32}
	if err := validateTerminalOpen(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, request := range []terminalOpenRequest{
		{ID: "../escape", Shell: "/bin/bash", Columns: 120, Rows: 32},
		{ID: "term_1", Shell: "/bin/sh", Columns: 120, Rows: 32},
		{ID: "term_1", Shell: "/bin/bash", Columns: 1, Rows: 1},
		{ID: "term_1", Hostname: "invalid.host", Shell: "/bin/bash", Columns: 120, Rows: 32},
	} {
		if err := validateTerminalOpen(request); err == nil {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}

func TestMergeEnvironmentAppliesTerminalAndSandboxValues(t *testing.T) {
	env := mergeEnvironment([]string{"PATH=/bin", "TERM=dumb"}, map[string]string{"PROJECT": "renderops"})
	joined := bytes.Join(func() [][]byte {
		items := make([][]byte, 0, len(env))
		for _, item := range env {
			items = append(items, []byte(item))
		}
		return items
	}(), []byte{0})
	for _, expected := range [][]byte{[]byte("PATH=/bin"), []byte("TERM=xterm-256color"), []byte("PROJECT=renderops")} {
		if !bytes.Contains(joined, expected) {
			t.Fatalf("environment %q missing %q", joined, expected)
		}
	}
}

func TestTerminalManagerStartsAndClosesPTY(t *testing.T) {
	manager := newTerminalManager(2)
	manager.workDir = t.TempDir()
	request := terminalOpenRequest{ID: "term_test", Shell: "/bin/bash", Columns: 80, Rows: 24}
	if err := manager.Open(request); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := manager.Close(request.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestTerminalManagerStreamsPTYOverAttachment(t *testing.T) {
	manager := newTerminalManager(2)
	manager.workDir = t.TempDir()
	request := terminalOpenRequest{ID: "term_stream", Shell: "/bin/bash", Columns: 80, Rows: 24}
	if err := manager.Open(request); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer manager.Close(request.ID)

	guest, host := net.Pipe()
	defer host.Close()
	attachDone := make(chan error, 1)
	go func() {
		defer guest.Close()
		attachDone <- manager.Attach(guest, request.ID)
	}()
	_ = host.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(host)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(line, &response); err != nil || !response.Success {
		t.Fatalf("attach response=%s err=%v", line, err)
	}
	if err := writeTerminalFrame(host, terminalWireResize, []byte{0, 100, 0, 30}); err != nil {
		t.Fatal(err)
	}
	if err := writeTerminalFrame(host, terminalWireInput, []byte("printf 'stream-ok\\n'; exit\n")); err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	for {
		frameType, payload, err := readTerminalFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		switch frameType {
		case terminalWireOutput:
			output.Write(payload)
		case terminalWireExit:
			if !strings.Contains(output.String(), "stream-ok") {
				t.Fatalf("terminal output %q missing command result", output.String())
			}
			if err := <-attachDone; err != nil {
				t.Fatalf("Attach: %v", err)
			}
			return
		}
	}
}
