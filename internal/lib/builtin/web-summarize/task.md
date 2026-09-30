---
output:
  type: json
---

Read the `_json` run input. It contains:

- `url` (required): absolute URL to summarize
- `focus` (optional): emphasis for the summary

Call `web.fetch_markdown` exactly once using the provided `url`.

If `web.fetch_markdown` returns a non-2xx `status` or empty `markdown`, do not fail. Return:

- the upstream `final_url`
- the upstream `status`
- the upstream `title`
- `summary: ""`
- `key_points: []`
- propagated `warnings`

Otherwise, return a concise structured summary:

- `summary`: short paragraph or two covering the most important information
- `key_points`: 3-6 short strings
- preserve and propagate any upstream `warnings`
- if `focus` is present, use it to steer emphasis without inventing facts

Return only JSON matching `schema.md`. Do not wrap the response in markdown fences or prose.
