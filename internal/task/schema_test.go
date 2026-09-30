package task

import (
	"errors"
	"testing"
)

func TestParseSchemaMD_RawJSON(t *testing.T) {
	content := []byte(`{"type": "object", "properties": {"name": {"type": "string"}}}`)
	schema, err := ParseSchemaMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("schema should not be nil")
	}
}

func TestParseSchemaMD_FencedBlock(t *testing.T) {
	content := []byte("Some description.\n\n```json\n{\"type\": \"object\"}\n```\n")
	schema, err := ParseSchemaMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("schema should not be nil")
	}
}

func TestParseSchemaMD_MultipleFencedBlocks(t *testing.T) {
	content := []byte("```json\n{\"type\": \"object\"}\n```\n\n```json\n{\"type\": \"array\"}\n```\n")
	schema, err := ParseSchemaMD(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("schema should not be nil")
	}
	// Should use first block
	if string(schema.Raw) != `{"type": "object"}` {
		t.Errorf("raw = %s, want first block", schema.Raw)
	}
}

func TestParseSchemaMD_InvalidJSON(t *testing.T) {
	content := []byte(`{not valid json}`)
	_, err := ParseSchemaMD(content)
	if !errors.Is(err, ErrInvalidSchema) {
		t.Errorf("err = %v, want ErrInvalidSchema", err)
	}
}

func TestParseSchemaMD_ValidJSONButInvalidSchema(t *testing.T) {
	// Valid JSON but uses invalid JSON Schema keyword value
	content := []byte(`{"type": "notavalidtype"}`)
	_, err := ParseSchemaMD(content)
	if !errors.Is(err, ErrInvalidSchema) {
		t.Errorf("err = %v, want ErrInvalidSchema for invalid schema type", err)
	}
}

func TestValidateSchemaOutputCoupling_JsonWithoutSchema(t *testing.T) {
	task := &Task{
		Frontmatter: Frontmatter{
			Output: &IOConfig{Type: "json"},
		},
		Schema: nil,
	}
	errs := ValidateTask(task)
	if !containsError(errs, ErrSchemaRequired) {
		t.Errorf("errs = %v, want ErrSchemaRequired", errs)
	}
}

func TestValidateSchemaOutputCoupling_SchemaWithoutJson(t *testing.T) {
	task := &Task{
		Frontmatter: Frontmatter{},
		Schema:      &JSONSchema{Raw: []byte(`{}`)},
	}
	errs := ValidateTask(task)
	if !containsError(errs, ErrSchemaForbidden) {
		t.Errorf("errs = %v, want ErrSchemaForbidden", errs)
	}
}

func containsError(errs []error, target error) bool {
	for _, err := range errs {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
