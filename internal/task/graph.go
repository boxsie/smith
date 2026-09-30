package task

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// GraphNode wraps a task with phase information for scheduling.
type GraphNode struct {
	Task   *Task
	Phase  string // "task" or "return"
	NodeID string // unique ID: taskID for task-phase, taskID + ":return" for return-phase
}

// TaskNodeID returns the node ID for a task's task-phase node.
func TaskNodeID(taskID string) string { return taskID }

// ReturnNodeID returns the node ID for a task's return-phase node.
func ReturnNodeID(taskID string) string { return taskID + ":return" }

// completionNodeID returns the node ID representing when a task is "done"
// from the perspective of dependents. For two-phase tasks this is the return
// node; for single-phase tasks it is the task node.
func completionNodeID(t *Task) string {
	if t.HasReturn {
		return ReturnNodeID(t.ID)
	}
	return TaskNodeID(t.ID)
}

// ExecutionLevel is a set of graph nodes that can run in parallel.
type ExecutionLevel []*GraphNode

// ExecutionPlan is a topologically sorted sequence of execution levels.
type ExecutionPlan []ExecutionLevel

// Graph holds the computed dependency DAG and execution plan.
type Graph struct {
	Plan        ExecutionPlan
	Deps        map[string][]*Task      // task ID → all predecessors (parent + sibling)
	SiblingDeps map[string][]*Task      // task ID → sibling deps only
	NodeDeps    map[string][]*GraphNode // node ID → predecessor nodes (for scheduler)
}

var numericPrefixRe = regexp.MustCompile(`^(\d+)-`)

// BuildGraph constructs a dependency DAG from a discovered task tree
// and returns a topologically sorted execution plan.
// The root task must have already been through DiscoverTree and ResolveAgentInheritance.
func BuildGraph(root *Task) (*Graph, error) {
	// Index all tasks by ID.
	taskIndex := make(map[string]*Task)
	WalkTree(root, func(t *Task) {
		taskIndex[t.ID] = t
	})

	// Create graph nodes. Every task gets a task-phase node.
	// Tasks with HasReturn also get a return-phase node.
	nodeIndex := make(map[string]*GraphNode)
	WalkTree(root, func(t *Task) {
		tn := &GraphNode{Task: t, Phase: "task", NodeID: TaskNodeID(t.ID)}
		nodeIndex[tn.NodeID] = tn
		if t.HasReturn {
			rn := &GraphNode{Task: t, Phase: "return", NodeID: ReturnNodeID(t.ID)}
			nodeIndex[rn.NodeID] = rn
		}
	})

	// Node-level adjacency: from → [to] (from must finish before to starts).
	adj := make(map[string][]string)
	inDegree := make(map[string]int)
	for nid := range nodeIndex {
		inDegree[nid] = 0
	}

	// Task-level deps (unchanged API for validation, summary, shell, cache).
	deps := make(map[string][]*Task)
	siblingDeps := make(map[string][]*Task)

	// Node-level deps (for scheduler skip logic).
	nodeDeps := make(map[string][]*GraphNode)

	addNodeEdge := func(from, to string) {
		adj[from] = append(adj[from], to)
		inDegree[to]++
	}

	// Compute all edges.
	WalkTree(root, func(t *Task) {
		// Parent-to-child edges: parent's task-phase → child's task-phase.
		for _, child := range t.Children {
			addNodeEdge(TaskNodeID(t.ID), TaskNodeID(child.ID))
			deps[child.ID] = append(deps[child.ID], t)
			nodeDeps[TaskNodeID(child.ID)] = append(nodeDeps[TaskNodeID(child.ID)], nodeIndex[TaskNodeID(t.ID)])
		}

		// Return-phase edges: each child's completion node → parent's return node.
		if t.HasReturn {
			returnNID := ReturnNodeID(t.ID)
			for _, child := range t.Children {
				childDone := completionNodeID(child)
				addNodeEdge(childDone, returnNID)
				nodeDeps[returnNID] = append(nodeDeps[returnNID], nodeIndex[childDone])
			}
		}

		// Sibling edges among children.
		if len(t.Children) == 0 {
			return
		}

		// Build sibling lookup by directory name.
		siblingByName := make(map[string]*Task, len(t.Children))
		for _, child := range t.Children {
			name := filepath.Base(child.Path)
			siblingByName[name] = child
		}

		// Explicit depends_on edges.
		hasExplicitDeps := make(map[string]bool)
		for _, child := range t.Children {
			if len(child.Frontmatter.DependsOn) == 0 {
				continue
			}
			hasExplicitDeps[child.ID] = true
			for _, depName := range child.Frontmatter.DependsOn {
				dep, ok := siblingByName[depName]
				if !ok {
					return // error handled in validation pass below
				}
				depDone := completionNodeID(dep)
				addNodeEdge(depDone, TaskNodeID(child.ID))
				deps[child.ID] = append(deps[child.ID], dep)
				siblingDeps[child.ID] = append(siblingDeps[child.ID], dep)
				nodeDeps[TaskNodeID(child.ID)] = append(nodeDeps[TaskNodeID(child.ID)], nodeIndex[depDone])
			}
		}

		// Validate depends_on references before implicit edges.
		for _, child := range t.Children {
			for _, depName := range child.Frontmatter.DependsOn {
				if _, ok := siblingByName[depName]; !ok {
					return // caught in validation pass below
				}
			}
		}

		// Implicit prefix edges for children without depends_on.
		type prefixGroup struct {
			prefix int
			tasks  []*Task
		}
		var numberedGroups []prefixGroup
		seen := make(map[int]int)

		for _, child := range t.Children {
			if hasExplicitDeps[child.ID] {
				continue
			}
			p := numericPrefix(child)
			if p == -1 {
				continue
			}
			if idx, ok := seen[p]; ok {
				numberedGroups[idx].tasks = append(numberedGroups[idx].tasks, child)
			} else {
				seen[p] = len(numberedGroups)
				numberedGroups = append(numberedGroups, prefixGroup{prefix: p, tasks: []*Task{child}})
			}
		}

		sort.Slice(numberedGroups, func(i, j int) bool {
			return numberedGroups[i].prefix < numberedGroups[j].prefix
		})

		for i := 1; i < len(numberedGroups); i++ {
			prevGroup := numberedGroups[i-1]
			currGroup := numberedGroups[i]
			for _, curr := range currGroup.tasks {
				for _, prev := range prevGroup.tasks {
					prevDone := completionNodeID(prev)
					addNodeEdge(prevDone, TaskNodeID(curr.ID))
					deps[curr.ID] = append(deps[curr.ID], prev)
					siblingDeps[curr.ID] = append(siblingDeps[curr.ID], prev)
					nodeDeps[TaskNodeID(curr.ID)] = append(nodeDeps[TaskNodeID(curr.ID)], nodeIndex[prevDone])
				}
			}
		}
	})

	// Validate depends_on references (separate pass for clean error reporting).
	var validationErr error
	WalkTree(root, func(t *Task) {
		if validationErr != nil {
			return
		}
		for _, child := range t.Children {
			siblingByName := make(map[string]*Task, len(t.Children))
			for _, sib := range t.Children {
				siblingByName[filepath.Base(sib.Path)] = sib
			}
			for _, depName := range child.Frontmatter.DependsOn {
				if _, ok := siblingByName[depName]; !ok {
					validationErr = fmt.Errorf("task %q: %w: %q", child.ID, ErrDependsOnNotFound, depName)
					return
				}
			}
		}
	})
	if validationErr != nil {
		return nil, validationErr
	}

	// Kahn's algorithm with leveled output, operating on NodeIDs.
	totalNodes := len(nodeIndex)
	var plan ExecutionPlan
	sortedCount := 0

	var queue []string
	for nid, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, nid)
		}
	}
	sort.Strings(queue)

	for len(queue) > 0 {
		level := make(ExecutionLevel, len(queue))
		for i, nid := range queue {
			level[i] = nodeIndex[nid]
		}
		plan = append(plan, level)
		sortedCount += len(queue)

		var nextQueue []string
		for _, nid := range queue {
			for _, neighbor := range adj[nid] {
				inDegree[neighbor]--
				if inDegree[neighbor] == 0 {
					nextQueue = append(nextQueue, neighbor)
				}
			}
		}
		sort.Strings(nextQueue)
		queue = nextQueue
	}

	if sortedCount < totalNodes {
		cycle := findCycle(taskIndex, inDegree)
		return nil, fmt.Errorf("%w: %v", ErrCycleDetected, cycle)
	}

	// Deduplicate deps and siblingDeps, preserving order.
	for id := range deps {
		deps[id] = dedup(deps[id])
	}
	for id := range siblingDeps {
		siblingDeps[id] = dedup(siblingDeps[id])
	}
	for nid := range nodeDeps {
		nodeDeps[nid] = dedupNodes(nodeDeps[nid])
	}

	return &Graph{
		Plan:        plan,
		Deps:        deps,
		SiblingDeps: siblingDeps,
		NodeDeps:    nodeDeps,
	}, nil
}

// numericPrefix extracts the numeric prefix from a task's directory name.
// Returns -1 if the task has no numeric prefix.
func numericPrefix(t *Task) int {
	name := filepath.Base(t.Path)
	m := numericPrefixRe.FindStringSubmatch(name)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

// findCycle walks nodes still in the graph (in-degree > 0) to find a cycle path.
// inDegree map may contain both task-level and node-level IDs; we filter to
// task-level IDs only (no ":return" suffix) for readable error messages.
func findCycle(index map[string]*Task, inDegree map[string]int) []string {
	remaining := make(map[string]bool)
	for id, deg := range inDegree {
		if deg > 0 {
			// Only include task-level IDs for cycle reporting.
			if _, ok := index[id]; ok {
				remaining[id] = true
			}
		}
	}

	visited := make(map[string]bool)
	onStack := make(map[string]bool)
	var cyclePath []string

	var dfs func(id string) bool
	dfs = func(id string) bool {
		visited[id] = true
		onStack[id] = true

		t := index[id]
		if t.Parent != nil {
			for _, sib := range t.Parent.Children {
				for _, dep := range sib.Frontmatter.DependsOn {
					if filepath.Base(t.Path) == dep && remaining[sib.ID] {
						if onStack[sib.ID] {
							cyclePath = append(cyclePath, sib.ID, id)
							return true
						}
						if !visited[sib.ID] {
							if dfs(sib.ID) {
								return true
							}
						}
					}
				}
			}
		}

		onStack[id] = false
		return false
	}

	var ids []string
	for id := range remaining {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if !visited[id] {
			if dfs(id) {
				return cyclePath
			}
		}
	}

	return ids
}

// dedup removes duplicate tasks from a slice, preserving order.
func dedup(tasks []*Task) []*Task {
	seen := make(map[string]bool, len(tasks))
	result := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		if !seen[t.ID] {
			seen[t.ID] = true
			result = append(result, t)
		}
	}
	return result
}

// dedupNodes removes duplicate graph nodes from a slice, preserving order.
func dedupNodes(nodes []*GraphNode) []*GraphNode {
	seen := make(map[string]bool, len(nodes))
	result := make([]*GraphNode, 0, len(nodes))
	for _, n := range nodes {
		if !seen[n.NodeID] {
			seen[n.NodeID] = true
			result = append(result, n)
		}
	}
	return result
}
