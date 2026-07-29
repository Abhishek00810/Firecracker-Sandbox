package platform

import "testing"

func TestMarshalSandboxMetadataUsesObjectForNil(t *testing.T) {
	metadata, err := marshalSandboxMetadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(metadata) != "{}" {
		t.Fatalf("metadata=%s want {}", metadata)
	}
}

func TestMarshalSandboxMetadataPreservesObject(t *testing.T) {
	metadata, err := marshalSandboxMetadata(map[string]any{"purpose": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if string(metadata) != `{"purpose":"test"}` {
		t.Fatalf("metadata=%s", metadata)
	}
}
