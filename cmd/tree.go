package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/dmotles/sprawl/internal/observe"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

type treeDeps struct {
	observeDeps observe.Deps
	getenv      func(string) string
}

var defaultTreeDeps *treeDeps

var (
	treeJSON bool
	treeRoot string
)

func init() {
	treeCmd.Flags().BoolVar(&treeJSON, "json", false, "Output as JSON")
	treeCmd.Flags().StringVar(&treeRoot, "root", "", "Show subtree rooted at the named agent")
	rootCmd.AddCommand(treeCmd)
}

var treeCmd = &cobra.Command{
	Use:   "tree",
	Short: "Display the agent hierarchy tree",
	RunE: func(cmd *cobra.Command, _ []string) error {
		deps := resolveTreeDeps()
		return runTree(deps, cmd.OutOrStdout(), treeJSON, treeRoot)
	},
}

func resolveTreeDeps() *treeDeps {
	if defaultTreeDeps != nil {
		return defaultTreeDeps
	}

	var runner tmux.Runner
	tmuxPath, err := tmux.FindTmux()
	if err == nil {
		runner = &tmux.RealRunner{TmuxPath: tmuxPath}
	}

	return &treeDeps{
		observeDeps: observe.Deps{
			TmuxRunner:    runner,
			ListAgents:    state.ListAgents,
			ReadRootName:  state.ReadRootName,
			ReadNamespace: state.ReadNamespace,
		},
		getenv: os.Getenv,
	}
}

func runTree(deps *treeDeps, stdout io.Writer, jsonOutput bool, subtreeRoot string) error {
	deprecationWarning("tree", "sprawl_status")
	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	agents, err := observe.LoadAll(deps.observeDeps, sprawlRoot)
	if err != nil {
		return fmt.Errorf("loading agents: %w", err)
	}

	rootName := deps.observeDeps.ReadRootName(sprawlRoot)
	root, orphans := observe.BuildTree(agents, rootName)

	// Handle --root flag: find subtree.
	if subtreeRoot != "" {
		node := findNode(root, subtreeRoot)
		if node == nil && orphans != nil {
			node = findNode(orphans, subtreeRoot)
		}
		if node == nil {
			return fmt.Errorf("agent %q not found", subtreeRoot)
		}
		root = node
		orphans = nil
	}

	if root == nil && orphans == nil {
		return nil
	}

	if jsonOutput {
		return renderJSON(stdout, root, orphans)
	}

	renderText(stdout, root, orphans)
	return nil
}

func findNode(node *observe.TreeNode, name string) *observe.TreeNode {
	if node == nil {
		return nil
	}
	if node.Agent != nil && node.Agent.Name == name {
		return node
	}
	for _, child := range node.Children {
		if found := findNode(child, name); found != nil {
			return found
		}
	}
	return nil
}

func renderText(w io.Writer, root *observe.TreeNode, orphans *observe.TreeNode) {
	if root != nil {
		fmt.Fprintln(w, formatLabel(root.Agent))
		for i, child := range root.Children {
			printNode(w, child, "", i == len(root.Children)-1)
		}
	}

	if orphans != nil && len(orphans.Children) > 0 {
		if root != nil {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "(orphaned)")
		for i, child := range orphans.Children {
			printNode(w, child, "", i == len(orphans.Children)-1)
		}
	}
}

func printNode(w io.Writer, node *observe.TreeNode, prefix string, isLast bool) {
	connector := "├── "
	if isLast {
		connector = "└── "
	}
	fmt.Fprintf(w, "%s%s%s\n", prefix, connector, formatLabel(node.Agent))

	childPrefix := prefix + "│   "
	if isLast {
		childPrefix = prefix + "    "
	}
	for i, child := range node.Children {
		printNode(w, child, childPrefix, i == len(node.Children)-1)
	}
}

func formatLabel(agent *observe.AgentInfo) string {
	if agent == nil {
		return "(unknown)"
	}

	name := agent.Name
	var role string
	switch {
	case agent.IsRoot:
		role = "root"
	case agent.Type != "" && agent.Family != "":
		role = agent.Type + "/" + agent.Family
	case agent.Type != "":
		role = agent.Type
	case agent.Family != "":
		role = agent.Family
	}

	status := agent.Status
	if status == "" {
		status = "active"
	}

	label := name + " (" + role
	if role != "" {
		label += ", "
	}
	label += status

	// Liveness: only for non-terminal statuses.
	if !observe.IsTerminal(status) {
		switch {
		case agent.ProcessAlive == nil:
			label += ", ?"
		case *agent.ProcessAlive:
			label += ", alive"
		default:
			label += ", DEAD"
		}
	}

	label += ")"
	return label
}

// JSON output types.
type jsonTreeOutput struct {
	Name         string           `json:"name"`
	Type         string           `json:"type"`
	Family       string           `json:"family"`
	Status       string           `json:"status"`
	ProcessAlive *bool            `json:"process_alive"`
	Children     []jsonTreeOutput `json:"children"`
	Orphans      []jsonTreeOutput `json:"orphans,omitempty"`
}

func renderJSON(w io.Writer, root *observe.TreeNode, orphans *observe.TreeNode) error {
	if root == nil {
		// No root, just orphans - shouldn't typically happen with --root flag.
		return nil
	}

	output := toJSONNode(root)

	if orphans != nil && len(orphans.Children) > 0 {
		for _, child := range orphans.Children {
			output.Orphans = append(output.Orphans, toJSONNode(child))
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(output)
}

func toJSONNode(node *observe.TreeNode) jsonTreeOutput {
	status := node.Agent.Status
	if status == "" {
		status = "active"
	}

	out := jsonTreeOutput{
		Name:         node.Agent.Name,
		Type:         node.Agent.Type,
		Family:       node.Agent.Family,
		Status:       status,
		ProcessAlive: node.Agent.ProcessAlive,
		Children:     []jsonTreeOutput{},
	}

	for _, child := range node.Children {
		out.Children = append(out.Children, toJSONNode(child))
	}

	return out
}
