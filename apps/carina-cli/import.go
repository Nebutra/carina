package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type importCandidate struct {
	Source            string   `json:"source"`
	ID                string   `json:"id"`
	Path              string   `json:"path"`
	WorkspaceRoot     string   `json:"workspace_root"`
	Title             string   `json:"title"`
	UpdatedAt         string   `json:"updated_at"`
	MessageCount      int      `json:"message_count"`
	Warnings          []string `json:"warnings"`
	ImportedSessionID string   `json:"imported_session_id"`
	ImportedMessages  int      `json:"imported_messages"`
	NewMessages       int      `json:"new_messages"`
	TargetWorkspace   string   `json:"target_workspace"`
	Importable        bool     `json:"importable"`
	ImportError       string   `json:"import_error"`
}

type importDiscovery struct {
	Conversations []importCandidate `json:"conversations"`
	Warnings      []string          `json:"warnings"`
	CopySemantics string            `json:"copy_semantics"`
}

type importReceipt struct {
	Source           string `json:"source"`
	ConversationID   string `json:"conversation_id"`
	SessionID        string `json:"session_id"`
	WorkspaceRoot    string `json:"workspace_root"`
	ImportedMessages int    `json:"imported_messages"`
	SkippedMessages  int    `json:"skipped_messages"`
	Status           string `json:"status"`
	Error            string `json:"error"`
}

type importApplyResult struct {
	Results []importReceipt `json:"results"`
}

type importOptions struct {
	Source        string
	SourceRoot    string
	WorkspaceRoot string
	AllWorkspaces bool
	JSON          bool
	All           bool
	IDs           []string
}

func cmdImport(c *rpcClient, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: carina import <list|apply> [options]")
	}
	action := args[0]
	if action != "list" && action != "apply" {
		return fmt.Errorf("usage: carina import <list|apply> [options]")
	}
	options, err := parseImportOptions(args[1:])
	if err != nil {
		return err
	}
	if options.WorkspaceRoot == "" && !options.AllWorkspaces {
		options.WorkspaceRoot, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	discovery, err := discoverImports(c, options)
	if err != nil {
		return err
	}
	if action == "list" {
		if options.JSON {
			return printJSON(discovery)
		}
		renderImportDiscovery(os.Stdout, discovery)
		return nil
	}
	if !options.All && len(options.IDs) == 0 {
		return fmt.Errorf("usage: carina import apply (--id source-id | --all) [options]")
	}
	selected, err := selectImportCandidates(discovery.Conversations, options)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return fmt.Errorf("no matching conversations to import")
	}
	selections := make([]map[string]any, 0, len(selected))
	for _, candidate := range selected {
		target := options.WorkspaceRoot
		if options.AllWorkspaces && target == "" {
			target = candidate.WorkspaceRoot
		}
		selection := map[string]any{
			"source": candidate.Source, "path": candidate.Path,
			"conversation_id": candidate.ID, "target_workspace": target,
		}
		if options.SourceRoot != "" {
			selection["source_root"] = options.SourceRoot
		}
		selections = append(selections, selection)
	}
	if !options.JSON {
		fmt.Fprintln(os.Stdout, "Carina will copy visible user and assistant messages from local history.")
		fmt.Fprintln(os.Stdout, "Source files stay unchanged. Later source changes are copied only when you run import again.")
		fmt.Fprintf(os.Stdout, "Importing %d conversation(s)...\n", len(selections))
	}
	var result importApplyResult
	if err := c.Call("conversation.import.apply", map[string]any{"selections": selections}, &result); err != nil {
		return err
	}
	if options.JSON {
		return printJSON(result)
	}
	renderImportReceipts(os.Stdout, result)
	return nil
}

func parseImportOptions(args []string) (importOptions, error) {
	var options importOptions
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--source", "--source-root", "--workspace", "--id":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return options, fmt.Errorf("%s requires a value", args[index])
			}
			index++
			switch args[index-1] {
			case "--source":
				if options.Source != "" {
					return options, fmt.Errorf("--source may be specified once")
				}
				options.Source = strings.TrimSpace(args[index])
			case "--source-root":
				options.SourceRoot = strings.TrimSpace(args[index])
			case "--workspace":
				options.WorkspaceRoot = strings.TrimSpace(args[index])
			case "--id":
				options.IDs = append(options.IDs, strings.TrimSpace(args[index]))
			}
		case "--all-workspaces":
			options.AllWorkspaces = true
		case "--all":
			options.All = true
		case "--json":
			options.JSON = true
		default:
			return options, fmt.Errorf("unknown import option %q", args[index])
		}
	}
	if options.Source != "" && options.Source != "claude-code" && options.Source != "codex" {
		return options, fmt.Errorf("--source must be claude-code or codex")
	}
	if options.SourceRoot != "" && options.Source == "" {
		return options, fmt.Errorf("--source-root requires --source")
	}
	if options.All && len(options.IDs) > 0 {
		return options, fmt.Errorf("--all and --id cannot be combined")
	}
	return options, nil
}

func discoverImports(c *rpcClient, options importOptions) (importDiscovery, error) {
	params := map[string]any{"all_workspaces": options.AllWorkspaces}
	if options.SourceRoot != "" {
		params["source_root"] = options.SourceRoot
	}
	if options.WorkspaceRoot != "" {
		params["workspace_root"] = options.WorkspaceRoot
	}
	if options.Source != "" {
		params["sources"] = []string{options.Source}
	}
	var discovery importDiscovery
	if err := c.Call("conversation.import.discover", params, &discovery); err != nil {
		return importDiscovery{}, err
	}
	sort.SliceStable(discovery.Conversations, func(i, j int) bool {
		if discovery.Conversations[i].Source != discovery.Conversations[j].Source {
			return discovery.Conversations[i].Source < discovery.Conversations[j].Source
		}
		return discovery.Conversations[i].ID < discovery.Conversations[j].ID
	})
	return discovery, nil
}

func selectImportCandidates(candidates []importCandidate, options importOptions) ([]importCandidate, error) {
	if options.All {
		selected := make([]importCandidate, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.Importable && candidate.NewMessages > 0 {
				selected = append(selected, candidate)
			}
		}
		return selected, nil
	}
	wanted := map[string]bool{}
	for _, id := range options.IDs {
		wanted[id] = true
	}
	var selected []importCandidate
	for _, candidate := range candidates {
		if wanted[candidate.ID] {
			if !candidate.Importable {
				reason := strings.TrimSpace(candidate.ImportError)
				if reason == "" {
					reason = "target workspace is unavailable"
				}
				return nil, fmt.Errorf("conversation %s cannot be imported: %s", candidate.ID, reason)
			}
			if candidate.NewMessages == 0 {
				return nil, fmt.Errorf("conversation %s is already up to date", candidate.ID)
			}
			selected = append(selected, candidate)
			delete(wanted, candidate.ID)
		}
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for id := range wanted {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("conversation id not found: %s", strings.Join(missing, ", "))
	}
	return selected, nil
}

func renderImportDiscovery(w io.Writer, discovery importDiscovery) {
	semantics := strings.TrimSpace(discovery.CopySemantics)
	if semantics == "" {
		semantics = "Carina copies selected local conversations and leaves source files unchanged."
	}
	fmt.Fprintln(w, semantics)
	if len(discovery.Conversations) == 0 {
		fmt.Fprintln(w, "No conversations found for this workspace.")
	} else {
		for _, candidate := range discovery.Conversations {
			state := "not imported"
			if !candidate.Importable {
				state = "unavailable"
				if candidate.ImportError != "" {
					state += ": " + candidate.ImportError
				}
			} else if candidate.ImportedSessionID != "" && candidate.NewMessages == 0 {
				state = "up to date"
			} else if candidate.ImportedSessionID != "" {
				state = fmt.Sprintf("%d new", candidate.NewMessages)
			}
			fmt.Fprintf(w, "%s  %s  %d messages  %s\n", candidate.Source, candidate.ID, candidate.MessageCount, state)
			fmt.Fprintf(w, "  %s\n  source workspace: %s\n", candidate.Title, candidate.WorkspaceRoot)
			if candidate.TargetWorkspace != "" {
				fmt.Fprintf(w, "  target workspace: %s\n", candidate.TargetWorkspace)
			}
		}
	}
	for _, warning := range discovery.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
}

func renderImportReceipts(w io.Writer, result importApplyResult) {
	for _, receipt := range result.Results {
		fmt.Fprintf(w, "%s %s: %s, imported=%d skipped=%d", receipt.Source, receipt.ConversationID, receipt.Status, receipt.ImportedMessages, receipt.SkippedMessages)
		if receipt.SessionID != "" {
			fmt.Fprintf(w, ", session=%s", receipt.SessionID)
		}
		if receipt.Error != "" {
			fmt.Fprintf(w, ", error=%s", receipt.Error)
		}
		fmt.Fprintln(w)
	}
}
