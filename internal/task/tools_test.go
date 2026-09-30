package task

import (
	"errors"
	"testing"
)

func TestParseToolsMD_Valid(t *testing.T) {
	content := []byte("- filesystem.read\n- web.fetch\n- code.execute\n")
	tools, err := ParseToolsMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("count = %d, want 3", len(tools))
	}
	want := []string{"filesystem.read", "web.fetch", "code.execute"}
	for i, w := range want {
		if tools[i] != w {
			t.Errorf("tools[%d] = %q, want %q", i, tools[i], w)
		}
	}
}

func TestParseToolsMD_Duplicate(t *testing.T) {
	content := []byte("- filesystem.read\n- filesystem.read\n")
	_, err := ParseToolsMD(content)
	if !errors.Is(err, ErrDuplicateTool) {
		t.Errorf("err = %v, want ErrDuplicateTool", err)
	}
}

func TestParseToolsMD_Empty(t *testing.T) {
	tools, err := ParseToolsMD([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("count = %d, want 0", len(tools))
	}
}

func TestParseToolsMD_MixedBullets(t *testing.T) {
	content := []byte("- filesystem.read\n* web.fetch\n- code.execute\n")
	tools, err := ParseToolsMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("count = %d, want 3", len(tools))
	}
}

func TestParseToolsMD_BlankLines(t *testing.T) {
	content := []byte("- filesystem.read\n\n- web.fetch\n\n")
	tools, err := ParseToolsMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("count = %d, want 2", len(tools))
	}
}

func TestParseToolsMD_ProseRejected(t *testing.T) {
	content := []byte("These are the tools:\n- filesystem.read\n")
	_, err := ParseToolsMD(content)
	if !errors.Is(err, ErrInvalidToolLine) {
		t.Errorf("err = %v, want ErrInvalidToolLine for prose line", err)
	}
}

func TestParseToolsMD_NestedBulletRejected(t *testing.T) {
	content := []byte("- filesystem.read\n  - filesystem.write\n")
	_, err := ParseToolsMD(content)
	if !errors.Is(err, ErrInvalidToolLine) {
		t.Errorf("err = %v, want ErrInvalidToolLine for nested bullet", err)
	}
}

func TestParseToolsMD_InvalidEntryNoBullet(t *testing.T) {
	content := []byte("filesystem.read\n")
	_, err := ParseToolsMD(content)
	if !errors.Is(err, ErrInvalidToolLine) {
		t.Errorf("err = %v, want ErrInvalidToolLine for bare tool ID without bullet", err)
	}
}
