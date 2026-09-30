{
  "type": "object",
  "required": ["tasks"],
  "properties": {
    "tasks": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "path", "purpose", "output_type"],
        "properties": {
          "id": {
            "type": "string",
            "description": "Task identifier (directory name, e.g. '01-extract')"
          },
          "path": {
            "type": "string",
            "description": "Relative path from project root (e.g. 'subtasks/01-extract')"
          },
          "purpose": {
            "type": "string",
            "description": "One-line description of what this task does"
          },
          "output_type": {
            "type": "string",
            "enum": ["markdown", "json"],
            "description": "Output format for this task"
          },
          "depends_on": {
            "type": "array",
            "items": { "type": "string" },
            "description": "Sibling task IDs this task depends on"
          },
          "has_return": {
            "type": "boolean",
            "description": "Whether this task has return.md for two-phase execution"
          },
          "has_schema": {
            "type": "boolean",
            "description": "Whether this task needs schema.md (required for json output)"
          },
          "model": {
            "type": "string",
            "description": "Model override for this task's agent.md (omit to inherit parent)"
          },
          "persona": {
            "type": "string",
            "description": "Persona override for this task's agent.md (omit to inherit parent)"
          },
          "tools": {
            "type": "array",
            "items": { "type": "string" },
            "description": "Tool IDs this task uses (from create_tools or use_existing_tools)"
          }
        }
      }
    },
    "create_tools": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["id", "description", "type", "behavior_spec", "input_fields"],
        "properties": {
          "id": {
            "type": "string",
            "description": "Dot-separated tool ID (e.g. 'issue.search')"
          },
          "description": {
            "type": "string",
            "description": "Human-readable description of what the tool does"
          },
          "type": {
            "type": "string",
            "enum": ["shell"],
            "description": "Tool type (only shell tools can be created at plan time)"
          },
          "behavior_spec": {
            "type": "string",
            "description": "Precise description of what run.sh should do — must be specific enough for implementation without ambiguity"
          },
          "timeout": {
            "type": "string",
            "description": "Duration override (e.g. '15s', '2m')"
          },
          "cache": {
            "type": "string",
            "enum": ["auto", "never"],
            "description": "Cache policy override"
          },
          "env": {
            "type": "array",
            "items": { "type": "string" },
            "description": "Required environment variables"
          },
          "input_fields": {
            "type": "array",
            "minItems": 1,
            "items": {
              "type": "object",
              "required": ["name", "type"],
              "properties": {
                "name": { "type": "string" },
                "type": { "type": "string" },
                "required": { "type": "boolean" },
                "description": { "type": "string" }
              }
            },
            "description": "Input schema fields"
          },
          "output_fields": {
            "type": "array",
            "items": {
              "type": "object",
              "required": ["name", "type"],
              "properties": {
                "name": { "type": "string" },
                "type": { "type": "string" },
                "description": { "type": "string" }
              }
            },
            "description": "Output schema fields (optional)"
          }
        }
      },
      "description": "New tools to create for this plan"
    },
    "use_existing_tools": {
      "type": "array",
      "items": { "type": "string" },
      "description": "Tool IDs from the project inventory to reuse"
    },
    "root_has_return": {
      "type": "boolean",
      "description": "Whether the root task needs return.md"
    },
    "root_model": {
      "type": "string",
      "description": "Model for the root agent.md"
    },
    "rationale": {
      "type": "string",
      "description": "Brief explanation of the design choice"
    }
  }
}
