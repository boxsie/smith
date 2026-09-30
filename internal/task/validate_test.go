package task

import "testing"

func TestValidateTask_ReturnNoChildren(t *testing.T) {
	task := &Task{
		ID:        "no-children",
		HasReturn: true,
		Children:  nil,
	}
	errs := ValidateTask(task)
	if !containsError(errs, ErrReturnNoChildren) {
		t.Errorf("errs = %v, want ErrReturnNoChildren", errs)
	}
}

func TestValidateTask_ShellReturn(t *testing.T) {
	child := &Task{ID: "child"}
	task := &Task{
		ID:             "shell-return",
		HasReturn:      true,
		EffectiveAgent: AgentConfig{Model: "shell"},
		Children:       []*Task{child},
	}
	errs := ValidateTask(task)
	if !containsError(errs, ErrShellReturn) {
		t.Errorf("errs = %v, want ErrShellReturn", errs)
	}
}

func TestValidateTask_ReturnWithChildren(t *testing.T) {
	child := &Task{ID: "child"}
	task := &Task{
		ID:             "valid-return",
		HasReturn:      true,
		EffectiveAgent: AgentConfig{Model: "anthropic/claude-sonnet-4-6"},
		Children:       []*Task{child},
	}
	errs := ValidateTask(task)
	if containsError(errs, ErrReturnNoChildren) {
		t.Error("should not have ErrReturnNoChildren when children exist")
	}
	if containsError(errs, ErrShellReturn) {
		t.Error("should not have ErrShellReturn for non-shell task")
	}
}

func TestValidateTask_NoReturnUnchanged(t *testing.T) {
	task := &Task{
		ID:        "no-return",
		HasReturn: false,
		Children:  nil,
	}
	errs := ValidateTask(task)
	if containsError(errs, ErrReturnNoChildren) {
		t.Error("should not validate return.md rules when HasReturn is false")
	}
	if containsError(errs, ErrShellReturn) {
		t.Error("should not validate return.md rules when HasReturn is false")
	}
}
