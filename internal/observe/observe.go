package observe

import (
	"sort"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// AgentInfo wraps AgentState with runtime liveness and role info.
type AgentInfo struct {
	state.AgentState
	ProcessAlive *bool
	IsRoot       bool
}

// Deps holds injected dependencies for the observe package.
type Deps struct {
	TmuxRunner    tmux.Runner
	ListAgents    func(sprawlRoot string) ([]*state.AgentState, error)
	ReadRootName  func(sprawlRoot string) string
	ReadNamespace func(sprawlRoot string) string
}

// TreeNode represents a node in the agent hierarchy tree.
type TreeNode struct {
	Agent    *AgentInfo
	Children []*TreeNode
}

// LoadAll loads all agents, synthesizes the root if needed, and annotates liveness.
// Agents are returned sorted by name for deterministic output.
func LoadAll(deps Deps, sprawlRoot string) ([]*AgentInfo, error) {
	agents, err := deps.ListAgents(sprawlRoot)
	if err != nil {
		return nil, err
	}

	rootName := deps.ReadRootName(sprawlRoot)
	namespace := deps.ReadNamespace(sprawlRoot)

	var result []*AgentInfo

	// Convert state agents to AgentInfo.
	for _, a := range agents {
		info := &AgentInfo{AgentState: *a}
		result = append(result, info)
	}

	// Synthesize root entry if root name is set.
	if rootName != "" {
		root := &AgentInfo{
			AgentState: state.AgentState{Name: rootName},
			IsRoot:     true,
		}
		result = append(result, root)
	}

	// Annotate liveness.
	for _, info := range result {
		if deps.TmuxRunner == nil {
			continue // ProcessAlive stays nil
		}
		if info.IsRoot {
			alive := deps.TmuxRunner.HasSession(tmux.RootSessionName(namespace, rootName))
			info.ProcessAlive = &alive
		} else {
			alive := deps.TmuxRunner.HasWindow(info.TmuxSession, info.TmuxWindow)
			info.ProcessAlive = &alive
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// BuildTree organizes agents into a tree by parent relationships.
// Returns the root node and a separate node containing orphaned agents
// whose parent is not found in the agent list. Orphans is nil if there are none.
func BuildTree(agents []*AgentInfo, rootName string) (root *TreeNode, orphans *TreeNode) {
	if len(agents) == 0 {
		return nil, nil
	}

	// Index agents by name.
	byName := make(map[string]*AgentInfo, len(agents))
	for _, a := range agents {
		byName[a.Name] = a
	}

	// Build parent -> children map.
	childrenOf := make(map[string][]*AgentInfo)
	var orphanList []*AgentInfo

	for _, a := range agents {
		if a.Name == rootName {
			continue // root is not a child of anyone
		}
		if a.Parent == "" || byName[a.Parent] == nil {
			orphanList = append(orphanList, a)
		} else {
			childrenOf[a.Parent] = append(childrenOf[a.Parent], a)
		}
	}

	// Build tree from root.
	rootAgent, ok := byName[rootName]
	if !ok {
		// Root not found — all agents are orphans.
		if len(agents) == 0 {
			return nil, nil
		}
		orphanList = agents
		orphanNode := &TreeNode{}
		for _, a := range orphanList {
			orphanNode.Children = append(orphanNode.Children, &TreeNode{Agent: a})
		}
		sortChildren(orphanNode.Children)
		return nil, orphanNode
	}

	root = buildSubtree(rootAgent, childrenOf)

	if len(orphanList) > 0 {
		orphans = &TreeNode{}
		for _, a := range orphanList {
			orphans.Children = append(orphans.Children, buildSubtree(a, childrenOf))
		}
		sortChildren(orphans.Children)
	}

	return root, orphans
}

func buildSubtree(agent *AgentInfo, childrenOf map[string][]*AgentInfo) *TreeNode {
	node := &TreeNode{Agent: agent}
	children := childrenOf[agent.Name]
	for _, child := range children {
		node.Children = append(node.Children, buildSubtree(child, childrenOf))
	}
	sortChildren(node.Children)
	return node
}

func sortChildren(children []*TreeNode) {
	sort.Slice(children, func(i, j int) bool {
		return children[i].Agent.Name < children[j].Agent.Name
	})
}

// IsTerminal reports whether the given status is a terminal state
// (done, problem, retiring) where liveness checks are not applicable.
func IsTerminal(status string) bool {
	switch status {
	case "done", "problem", "retiring":
		return true
	}
	return false
}
