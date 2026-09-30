{
  "type": "object",
  "required": ["operations"],
  "properties": {
    "operations": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["op", "path", "content"],
        "properties": {
          "op": {
            "type": "string",
            "enum": ["write"],
            "description": "Operation type (always write for tool creation)"
          },
          "path": {
            "type": "string",
            "description": "File path scoped to tools/<tool_id>/..."
          },
          "content": {
            "type": "string",
            "description": "Complete file content"
          }
        }
      }
    }
  }
}
