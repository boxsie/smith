---
---

Given the goal and the project summary (both from run input), compress the project summary into a compact context artifact for downstream planning.

The only document you are compressing is the `project_summary` run input. Do not summarize parent context files, planner guidance documents, or model-selection reference text as if they were part of the target project.

Anchor on the user's goal. Summarize the target project, not Smith's planner, its stages, or parent guidance text.

Preserve verbatim:
- The goal itself, in a short `Goal` section
- Structural invariants (file paths, dependency edges, sidecar presence)
- Constraints and rules
- Existing task bodies and schema definitions that already exist on disk and must be preserved exactly

Discard:
- Output directory contents (runtime artifacts)
- Redundant structural information
- Binary files and large data files
- Goal-specific examples or proposals invented by parent stages
- Planner-internal guidance that does not describe the target project on disk
- Parent context filenames or sections (for example planner guidance docs) unless they are explicitly present inside the target project's own files

Do not copy full task bodies, full schema documents, or full file contents unless they already exist in the input project and are essential to preserve verbatim. Prefer compact summaries of purpose, shape, and required fields.

For empty or simple projects, keep the result short. A few labeled sections or bullets is enough.

Produce one compact summary in markdown with labeled sections. Do not iterate.
