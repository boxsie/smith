package proposal

// ToolSpec describes a tool the planner wants to create.
// It matches the create_tools schema from the planner's 02-design output.
type ToolSpec struct {
	ID           string          `json:"id"`
	Description  string          `json:"description"`
	Type         string          `json:"type"`
	BehaviorSpec string          `json:"behavior_spec"`
	Timeout      string          `json:"timeout,omitempty"`
	Cache        string          `json:"cache,omitempty"`
	Env          []string        `json:"env,omitempty"`
	InputFields  []ToolFieldSpec `json:"input_fields"`
	OutputFields []ToolFieldSpec `json:"output_fields,omitempty"`
}

// ToolFieldSpec describes a single field in a tool's input or output schema.
type ToolFieldSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Description string `json:"description,omitempty"`
}
