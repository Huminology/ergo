package ergo

import (
	"testing"
)

func TestFilterOpenForestRootsKeepsLiveForestAndBodyNestedEpics(t *testing.T) {
	graph := &Graph{
		Tasks: map[string]*Task{
			"OPEN01": {ID: "OPEN01", Title: "open landing"},
			"TODO01": {ID: "TODO01", Title: "todo", State: stateTodo, EpicID: "OPEN01"},
			"DONE01": {ID: "DONE01", Title: "done child", State: stateDone, EpicID: "OPEN01"},
			"GATE01": {ID: "GATE01", Title: "gateway", State: stateDone, EpicID: "OPEN01", Body: "nestedProcedureId: NEST01\n"},
			"NEST01": {ID: "NEST01", Title: "run:ci"},
			"NTASK1": {ID: "NTASK1", Title: "nested done", State: stateDone, EpicID: "NEST01"},
			"OLD001": {ID: "OLD001", Title: "finished landing"},
			"OLDT01": {ID: "OLDT01", Title: "old task", State: stateDone, EpicID: "OLD001"},
		},
		Deps: map[string]map[string]struct{}{},
	}
	graph.rebuildIndexes()
	graph.prepareDerivedQueries()

	roots := buildListRoots(graph, ListOptions{OpenForests: true})
	got := map[string]bool{}
	var walk func([]*treeNode)
	walk = func(nodes []*treeNode) {
		for _, node := range nodes {
			if node == nil || node.task == nil {
				continue
			}
			got[node.task.ID] = true
			walk(node.children)
		}
	}
	walk(roots)

	for _, id := range []string{"OPEN01", "TODO01", "DONE01", "GATE01", "NEST01", "NTASK1"} {
		if !got[id] {
			t.Errorf("open forests missing %s", id)
		}
	}
	if got["OLD001"] || got["OLDT01"] {
		t.Errorf("open forests leaked closed forest: %v", got)
	}
}

func TestFilterOpenForestRootsDropsTerminalOrphans(t *testing.T) {
	graph := &Graph{
		Tasks: map[string]*Task{
			"LIVE01": {ID: "LIVE01", Title: "orphan", State: stateTodo},
			"DEAD01": {ID: "DEAD01", Title: "old orphan", State: stateDone},
		},
		Deps: map[string]map[string]struct{}{},
	}
	graph.rebuildIndexes()
	graph.prepareDerivedQueries()
	roots := buildListRoots(graph, ListOptions{OpenForests: true})
	if len(roots) != 1 || roots[0].task.ID != "LIVE01" {
		t.Fatalf("roots = %#v", roots)
	}
}
