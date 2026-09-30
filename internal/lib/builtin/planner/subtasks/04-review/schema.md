{
  "type": "object",
  "required": ["summary", "operations"],
  "properties": {
    "summary": {
      "type": "string",
      "description": "Human-readable description of what the proposed task tree does"
    },
    "operations": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["op", "path"],
        "properties": {
          "op": {
            "type": "string",
            "enum": ["write", "delete"]
          },
          "path": {
            "type": "string",
            "description": "File path relative to the project root"
          },
          "content": {
            "type": "string",
            "description": "File content for write operations"
          }
        },
        "if": { "properties": { "op": { "const": "write" } } },
        "then": { "required": ["content"] },
        "else": { "not": { "required": ["content"] } }
      }
    },
    "create_tools": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "description", "type", "behavior_spec", "input_fields"],
        "properties": {
          "id": { "type": "string" },
          "description": { "type": "string" },
          "type": { "type": "string", "enum": ["shell"] },
          "behavior_spec": { "type": "string" },
          "timeout": { "type": "string" },
          "cache": { "type": "string", "enum": ["auto", "never"] },
          "env": { "type": "array", "items": { "type": "string" } },
          "input_fields": {
            "type": "array", "minItems": 1,
            "items": {
              "type": "object", "required": ["name", "type"],
              "properties": {
                "name": { "type": "string" },
                "type": { "type": "string" },
                "required": { "type": "boolean" },
                "description": { "type": "string" }
              }
            }
          },
          "output_fields": {
            "type": "array",
            "items": {
              "type": "object", "required": ["name", "type"],
              "properties": {
                "name": { "type": "string" },
                "type": { "type": "string" },
                "description": { "type": "string" }
              }
            }
          }
        }
      },
      "description": "Tool specs from 02-design, passed through unchanged"
    },
    "use_existing_tools": {
      "type": "array",
      "items": { "type": "string" },
      "description": "Existing tool IDs from 02-design, passed through unchanged"
    }
  }
}
