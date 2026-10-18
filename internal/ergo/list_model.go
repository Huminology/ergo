package ergo

import (
	"regexp"
	"sort"
	"strings"
)

// ANSI color codes
const (
	colorReset   = "\033[0m"
	colorBold    = "\033[1m"
	colorDim     = "\033[2m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorCyan    = "\033[36m"
	colorRed     = "\033[31m"
	colorMagenta = "\033[35m"
)

const (
	// Layout contract: every task ID starts in column one, followed by a fixed
	// gap before tree structure and presentation. Width math uses visibleLen.
	idContentGap = 2

	// Keep the tree readable on ultrawide terminals; narrower terminals still
	// determine their own available width.
	maxListWidth = 120
)

// State icons
const (
	iconDone     = "✓"
	iconReady    = "○"
	iconDraft    = "◌"
	iconWaiting  = "◷"
	iconDoing    = "↻"
	iconBlocked  = "!"
	iconFailed   = "✗"
	iconCanceled = "–"
	iconError    = "⚠"
	iconEpic     = "◈"
)

// treeNode represents a task or epic in the tree structure.
type treeNode struct {
	task           *Task
	children       []*treeNode
	isEpic         bool
	isReady        bool
	collapsed      bool // for done epics: show summary instead of children
	collapsedCount int  // number of tasks in collapsed epic
}

// buildListRoots applies list-view filters to a tree and returns the roots to render.
func buildListRoots(graph *Graph, opts ListOptions) []*treeNode {
	var roots []*treeNode
	explicitEpic := opts.EpicID != ""
	if explicitEpic {
		if root := buildEpicTree(graph, opts.EpicID); root != nil {
			roots = []*treeNode{root}
		}
	} else {
		// Build tree structure: epics contain their tasks, and epics nest by dependency
		roots = buildTree(graph)
	}

	if explicitEpic {
		if len(roots) == 1 {
			// Epic-focused view shows all epic children by default; --ready is the only
			// implicit filter. (--all is accepted but redundant here.)
			roots[0].children = filterEpicChildrenForList(roots[0].children, graph, opts.ShowAll || opts.OpenForests || !opts.ReadyOnly, opts.ReadyOnly)
		}
	} else {
		// Apply ready filter first (drop epics with no matching children).
		if opts.ReadyOnly {
			roots = filterNodesByReady(roots, graph)
		} else if opts.OpenForests {
			roots = filterOpenForestRoots(graph, roots)
		} else if !opts.ShowAll {
			// Compute derived state for epics and filter unless --all
			roots = filterAndCollapseNodes(roots)
		}
	}

	if opts.ReadyOnly {
		sortReadyNodesByClaimOrder(roots)
	}

	return roots
}

func isTerminalListState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case stateDone, stateCanceled, "cancelled":
		return true
	default:
		return false
	}
}

func collectTasksFromNodes(nodes []*treeNode) []*Task {
	var out []*Task
	var walk func([]*treeNode)
	walk = func(ns []*treeNode) {
		for _, node := range ns {
			if node == nil || node.task == nil {
				continue
			}
			if !node.isEpic {
				out = append(out, node.task)
			}
			walk(node.children)
		}
	}
	walk(nodes)
	return out
}

var (
	openForestParentRe  = regexp.MustCompile(`(?m)^Parent:\s+(\S+)\s*$`)
	openForestNestedRe  = regexp.MustCompile(`(?m)^(?: {0,2})nestedProcedureId:\s*['"]?([A-Z0-9]{5,8})['"]?\s*$`)
	openForestAttemptRe = regexp.MustCompile(`(?m)^(?: {0,2})attemptEpicIds:\s*\[([^\]]*)\]`)
	openForestIDRe      = regexp.MustCompile(`[A-Z0-9]{5,8}`)
)

func referencedEpicIDsInBody(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var ids []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if m := openForestParentRe.FindStringSubmatch(body); len(m) > 1 {
		add(m[1])
	}
	for _, m := range openForestNestedRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range openForestAttemptRe.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			for _, id := range openForestIDRe.FindAllString(m[1], -1) {
				add(id)
			}
		}
	}
	return ids
}

// filterOpenForestRoots keeps non-terminal top-level epics (and active orphans)
// plus any epic reachable from those forests via body references (nested
// procedure / attempt epics), including terminal descendants.
func filterOpenForestRoots(graph *Graph, roots []*treeNode) []*treeNode {
	if graph == nil {
		return nil
	}
	byID := make(map[string]*treeNode, len(roots))
	for _, root := range roots {
		if root == nil || root.task == nil {
			continue
		}
		byID[root.task.ID] = root
	}
	kept := map[string]struct{}{}
	var queue []string
	enqueue := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, seen := kept[id]; seen {
			return
		}
		if byID[id] == nil && graph.Tasks[id] == nil {
			return
		}
		kept[id] = struct{}{}
		queue = append(queue, id)
	}
	for _, root := range roots {
		if root == nil || root.task == nil {
			continue
		}
		state := root.task.State
		if root.isEpic {
			state = graph.EpicState(root.task.ID)
		}
		if isTerminalListState(state) {
			continue
		}
		enqueue(root.task.ID)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		node := byID[id]
		task := graph.Tasks[id]
		if task != nil {
			for _, ref := range referencedEpicIDsInBody(task.Body) {
				enqueue(ref)
			}
			if task.EpicID != "" {
				enqueue(task.EpicID)
			}
		}
		if node == nil {
			continue
		}
		for _, child := range node.children {
			if child == nil || child.task == nil {
				continue
			}
			enqueue(child.task.ID)
		}
	}
	out := make([]*treeNode, 0, len(kept))
	for _, root := range roots {
		if root == nil || root.task == nil {
			continue
		}
		if _, ok := kept[root.task.ID]; ok {
			out = append(out, root)
		}
	}
	return out
}

// sortReadyNodesByClaimOrder makes the first visible leaf the task that an
// automatic claim would select. Each subtree is keyed by its oldest ready leaf.
func sortReadyNodesByClaimOrder(nodes []*treeNode) {
	for _, node := range nodes {
		sortReadyNodesByClaimOrder(node.children)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		left := oldestTaskInTree(nodes[i])
		right := oldestTaskInTree(nodes[j])
		if left == nil || right == nil {
			return right != nil
		}
		if left.CreatedAt.Equal(right.CreatedAt) {
			return left.ID < right.ID
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})
}

func oldestTaskInTree(node *treeNode) *Task {
	if node == nil || node.task == nil {
		return nil
	}
	if !node.isEpic {
		return node.task
	}
	var oldest *Task
	for _, child := range node.children {
		candidate := oldestTaskInTree(child)
		if candidate == nil {
			continue
		}
		if oldest == nil || candidate.CreatedAt.Before(oldest.CreatedAt) ||
			(candidate.CreatedAt.Equal(oldest.CreatedAt) && candidate.ID < oldest.ID) {
			oldest = candidate
		}
	}
	return oldest
}

func buildEpicTree(graph *Graph, epicID string) *treeNode {
	epic := graph.Tasks[epicID]
	if epic == nil || !graph.IsEpic(epicID) {
		return nil
	}
	node := &treeNode{task: epic, isEpic: true, isReady: graph.IsReady(epic.ID)}

	tasks := graph.Children(epicID)
	tasks = topoSortTasks(tasks, graph)
	for _, t := range tasks {
		node.children = append(node.children, &treeNode{
			task:    t,
			isReady: graph.IsReady(t.ID),
		})
	}
	return node
}

func filterEpicChildrenForList(children []*treeNode, graph *Graph, showAll bool, readyOnly bool) []*treeNode {
	filtered := make([]*treeNode, 0, len(children))
	for _, child := range children {
		if child == nil || child.task == nil {
			continue
		}
		if readyOnly {
			if !graph.IsReady(child.task.ID) {
				continue
			}
		} else if !showAll {
			if child.task.State == stateDone || child.task.State == stateCanceled {
				continue
			}
		}
		filtered = append(filtered, child)
	}
	return filtered
}

// filterNodesByReady keeps only ready tasks and epics with ready children.
// Call this only when --ready is requested.
func filterNodesByReady(nodes []*treeNode, graph *Graph) []*treeNode {
	filtered := make([]*treeNode, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || node.task == nil {
			continue
		}
		if node.isEpic {
			node.children = filterNodesByReady(node.children, graph)
			if len(node.children) == 0 {
				continue
			}
			filtered = append(filtered, node)
			continue
		}
		if !graph.IsReady(node.task.ID) {
			continue
		}
		filtered = append(filtered, node)
	}
	return filtered
}

// derivedEpicState computes an epic's state from its child tasks.
// Returns a derived presentation state for an epic's children.
func derivedEpicState(children []*treeNode) string {
	tasks := make([]*Task, 0, len(children))
	for _, child := range children {
		if child.task != nil {
			tasks = append(tasks, child.task)
		}
	}
	return derivedEpicStateForTasks(tasks)
}

// filterAndCollapseNodes filters tasks for the active view (default list output).
// UX goals for supervisors monitoring agent progress:
// - Show progress within active epics (done tasks visible for context)
// - Hide successful or canceled finished epics; keep failed outcomes visible
// - Hide orphan done tasks (standalone completed work)
// - Hide all canceled tasks (abandoned work)
func filterAndCollapseNodes(nodes []*treeNode) []*treeNode {
	return filterAndCollapseNodesImpl(nodes, false)
}

func filterAndCollapseNodesImpl(nodes []*treeNode, withinEpic bool) []*treeNode {
	var filtered []*treeNode
	for _, node := range nodes {
		// For epics, check derived state BEFORE filtering children
		if node.task != nil && node.isEpic {
			state := derivedEpicState(node.children)
			switch state {
			case "canceled":
				// Hide fully-canceled epics
				continue
			case stateDone:
				// Hide fully-done epics in active view
				continue
			case stateFailed:
				// Keep failed epics visible for investigation.
			case "empty":
				// Empty epics are shown as-is
				filtered = append(filtered, node)
				continue
			}
			// "active" epics: filter children and continue
		}

		// Recursively process children
		// If this node is an epic, its children should preserve done tasks for progress visibility
		if node.task != nil && node.isEpic {
			node.children = filterAndCollapseNodesImpl(node.children, true)
		} else {
			node.children = filterAndCollapseNodesImpl(node.children, withinEpic)
		}

		// Always skip canceled tasks (abandoned work)
		if node.task != nil && !node.isEpic && node.task.State == stateCanceled {
			continue
		}

		// Skip orphan done tasks (completed work not within an active epic)
		// Keep done tasks within epics to show progress: "3 of 5 done"
		if node.task != nil && !node.isEpic && !withinEpic && node.task.State == stateDone {
			continue
		}

		filtered = append(filtered, node)
	}
	return filtered
}

// countTasks counts non-epic tasks in a node tree.
func countTasks(nodes []*treeNode) int {
	count := 0
	for _, node := range nodes {
		if node.task != nil && !node.isEpic {
			count++
		}
		count += countTasks(node.children)
	}
	return count
}

// taskStats holds aggregate counts for the summary line.
type taskStats struct {
	ready      int
	draft      int
	inProgress int
	blocked    int
	waiting    int
	errors     int
	failed     int
	done       int
	canceled   int
	total      int
}

type summaryBucket int

const (
	summaryReady summaryBucket = iota
	summaryDraft
	summaryInProgress
	summaryBlocked
	summaryWaiting
	summaryError
	summaryFailed
	summaryDone
	summaryCanceled
)

func computeStatsForTasks(tasks []*Task, graph *Graph) taskStats {
	var stats taskStats
	for _, task := range tasks {
		if task == nil || graph.IsEpic(task.ID) {
			continue
		}
		stats.total++
		switch task.State {
		case stateDone:
			stats.done++
		case stateCanceled:
			stats.canceled++
		case stateFailed:
			stats.failed++
		case stateError:
			stats.errors++
		case stateDoing:
			stats.inProgress++
		case stateBlocked:
			stats.blocked++
		case stateTodo:
			if graph.IsReady(task.ID) {
				stats.ready++
			} else {
				stats.waiting++
			}
		case stateDraft:
			stats.draft++
		default:
			stats.blocked++
		}
	}
	return stats
}

// buildTree constructs a forest of tree nodes from the graph.
// All epics appear as siblings at root level (flat structure).
// Tasks are nested under their owning epic.
// Orphan tasks (no epic) appear at root level.
// Dependencies between epics are shown via ⧗ annotations, not nesting.
func buildTree(graph *Graph) []*treeNode {
	// Separate epics and tasks
	var epics, orphanTasks []*Task
	epicTasks := make(map[string][]*Task) // epicID -> tasks

	for _, task := range graph.Tasks {
		if graph.IsEpic(task.ID) {
			epics = append(epics, task)
		} else if task.EpicID == "" {
			orphanTasks = append(orphanTasks, task)
		} else {
			epicTasks[task.EpicID] = append(epicTasks[task.EpicID], task)
		}
	}

	// Sort epics by topo order: dependencies first (what you do first appears first)
	epics = topoSortTasks(epics, graph)

	// Build epic nodes with their tasks (flat - all epics at root level)
	var rootEpics []*treeNode

	for _, epic := range epics {
		node := &treeNode{task: epic, isEpic: true, isReady: graph.IsReady(epic.ID)}

		// Add tasks under this epic, sorted by dependency order
		tasks := epicTasks[epic.ID]
		tasks = topoSortTasks(tasks, graph)
		for _, t := range tasks {
			childNode := &treeNode{
				task:    t,
				isReady: graph.IsReady(t.ID),
			}
			node.children = append(node.children, childNode)
		}

		rootEpics = append(rootEpics, node)
	}

	// Build orphan task nodes
	var orphanNodes []*treeNode
	orphanTasks = topoSortTasks(orphanTasks, graph)
	for _, t := range orphanTasks {
		orphanNodes = append(orphanNodes, &treeNode{
			task:    t,
			isReady: graph.IsReady(t.ID),
		})
	}

	// Combine: orphan tasks first, then epic trees
	var roots []*treeNode
	roots = append(roots, orphanNodes...)
	roots = append(roots, rootEpics...)

	return roots
}

// topoSortTasks sorts tasks by dependency order (dependencies before dependents).
// Tasks with no dependencies come first, sorted by readiness then ID.
func topoSortTasks(tasks []*Task, graph *Graph) []*Task {
	if len(tasks) == 0 {
		return tasks
	}

	// Build in-degree map for these tasks only
	taskSet := make(map[string]bool)
	for _, t := range tasks {
		taskSet[t.ID] = true
	}

	inDegree := make(map[string]int)
	for _, t := range tasks {
		inDegree[t.ID] = 0
	}
	for _, t := range tasks {
		for _, depID := range graph.Dependencies(t.ID) {
			if taskSet[depID] {
				inDegree[t.ID]++
			}
		}
	}

	// Kahn's algorithm
	var queue []*Task
	for _, t := range tasks {
		if inDegree[t.ID] == 0 {
			queue = append(queue, t)
		}
	}

	// Sort initial queue: ready first, then by ID
	sort.Slice(queue, func(i, j int) bool {
		iReady := graph.IsReady(queue[i].ID)
		jReady := graph.IsReady(queue[j].ID)
		if iReady != jReady {
			return iReady // ready tasks first
		}
		return queue[i].ID < queue[j].ID
	})

	var result []*Task
	for len(queue) > 0 {
		// Pop first
		t := queue[0]
		queue = queue[1:]
		result = append(result, t)

		// Reduce in-degree of dependents in this task set.
		for _, dependentID := range graph.Dependents(t.ID) {
			if !taskSet[dependentID] {
				continue
			}
			inDegree[dependentID]--
			if inDegree[dependentID] == 0 {
				queue = append(queue, graph.Tasks[dependentID])
			}
		}

		// Re-sort queue
		sort.Slice(queue, func(i, j int) bool {
			iReady := graph.IsReady(queue[i].ID)
			jReady := graph.IsReady(queue[j].ID)
			if iReady != jReady {
				return iReady
			}
			return queue[i].ID < queue[j].ID
		})
	}

	return result
}
