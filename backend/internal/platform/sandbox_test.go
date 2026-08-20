package platform

import (
	"strings"
	"testing"
)

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

func TestSandboxInsertQueriesCastIdleTimeoutConsistently(t *testing.T) {
	for name, query := range map[string]string{
		"insert": insertSandboxQuery,
		"upsert": upsertSandboxQuery,
	} {
		if count := strings.Count(query, "$12::integer"); count != 2 {
			t.Errorf("%s query has %d explicit idle-timeout casts, want 2", name, count)
		}
		if strings.Contains(query, "$12::bigint") {
			t.Errorf("%s query casts the integer column parameter to bigint", name)
		}
	}
}
