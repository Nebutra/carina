package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/workflowui"
)

// cmdWorkflow is the `carina workflow ...` entry point — the productization
// gap this closes: workflow.run/list/detail/pause/resume/stop/restart were
// previously only reachable via raw RPC or the model-driven "workflow" tool
// call, with no CLI surface at all (unlike worker.*, session.*, channel.*,
// which all already have one).
func cmdWorkflow(c *rpcClient, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: carina workflow <run|list|status|pause|resume|stop|restart> ...")
	}
	switch args[0] {
	case "run":
		return cmdWorkflowRun(c, args[1:])
	case "list", "ls":
		return cmdWorkflowList(c, args[1:])
	case "status":
		return cmdWorkflowStatus(c, args[1:])
	case "pause":
		return callArg(c, "workflow.pause", args[1:], "run_id")
	case "resume":
		return callArg(c, "workflow.resume", args[1:], "run_id")
	case "stop":
		return callArg(c, "workflow.stop", args[1:], "run_id")
	case "restart":
		return callArg(c, "workflow.restart", args[1:], "run_id")
	default:
		return fmt.Errorf("unknown workflow subcommand %q", args[0])
	}
}

// parseWorkflowRunArgs pulls --session/--json/--background out of a
// positional [name] [input] argument list, mirroring parseRunArgs' inline
// switch style for --model/--agent in main.go.
func parseWorkflowRunArgs(args []string) (name, input, sessionID string, jsonOutput, background bool, err error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", "", false, false, fmt.Errorf("--session requires a session id")
			}
			sessionID = strings.TrimSpace(args[i+1])
			i++
		case "--json":
			jsonOutput = true
		case "--background":
			background = true
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) < 1 || strings.TrimSpace(rest[0]) == "" {
		return "", "", "", false, false, fmt.Errorf(`usage: carina workflow run <name> ["input"] [--session <id>] [--json] [--background]`)
	}
	name = strings.TrimSpace(rest[0])
	if len(rest) > 1 {
		input = rest[1]
	}
	return name, input, sessionID, jsonOutput, background, nil
}

func cmdWorkflowRun(c *rpcClient, args []string) error {
	name, input, sessionID, jsonOutput, background, err := parseWorkflowRunArgs(args)
	if err != nil {
		return err
	}
	if sessionID == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		var sess struct {
			SessionID string `json:"session_id"`
		}
		if err := c.Call("session.create", map[string]any{"workspace_root": cwd, "profile": "safe-edit"}, &sess); err != nil {
			return err
		}
		sessionID = sess.SessionID
		fmt.Printf("session: %s (safe-edit, %s)\n", sessionID, cwd)
	}
	var run workflowui.Run
	if err := c.Call("workflow.run", map[string]any{"session_id": sessionID, "workflow": name, "input": input}, &run); err != nil {
		return err
	}
	if jsonOutput {
		return printJSON(run)
	}
	fmt.Printf("workflow run started: %s (workflow=%q)\n", run.ID, name)
	if background || run.ID == "" {
		fmt.Printf("check progress with: carina workflow status %s\n", run.ID)
		return nil
	}
	fmt.Println("waiting for the run to finish (ctrl-c to stop watching; the run keeps going) ...")
	return runWaitForWorkflow(c, run.ID, workflowWaitDefaultTimeout, nil)
}

func cmdWorkflowList(c *rpcClient, args []string) error {
	jsonOutput := hasFlag(args, "--json")
	var runs []workflowui.Run
	if err := c.Call("workflow.list", map[string]any{}, &runs); err != nil {
		return err
	}
	if jsonOutput {
		return printJSON(runs)
	}
	if len(runs) == 0 {
		fmt.Println("no workflow runs")
		return nil
	}
	fmt.Printf("%-24s %-20s %-11s %-8s %s\n", "RUN", "WORKFLOW", "STATUS", "ATTEMPT", "UPDATED")
	for _, r := range runs {
		fmt.Printf("%-24s %-20s %-11s %-8d %s\n", r.ID, truncateForTable(r.Workflow, 20), r.Status, r.Attempt, r.UpdatedAt.Format(time.RFC3339))
	}
	return nil
}

func cmdWorkflowStatus(c *rpcClient, args []string) error {
	jsonOutput := hasFlag(args, "--json")
	args = dropFlag(args, "--json")
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: carina workflow status <run_id> [--json]")
	}
	var detail workflowui.Detail
	if err := c.Call("workflow.detail", map[string]any{"run_id": strings.TrimSpace(args[0])}, &detail); err != nil {
		return err
	}
	if jsonOutput {
		return printJSON(detail)
	}
	renderWorkflowDetail(os.Stdout, detail)
	return nil
}

// renderWorkflowDetail is the human-readable counterpart to
// workflow_progress_rollup (go/daemon/workflow_streaming.go): the same
// completed/failed/skipped/running/queued + budget shape a swarm run
// already emits on the event bus, finally with somewhere for an operator to
// actually look at it from the command line.
func renderWorkflowDetail(w io.Writer, d workflowui.Detail) {
	fmt.Fprintf(w, "run:      %s\n", d.Run.ID)
	fmt.Fprintf(w, "workflow: %s\n", d.Run.Workflow)
	fmt.Fprintf(w, "status:   %s\n", d.Run.Status)
	fmt.Fprintf(w, "progress: %.0f%% (%d/%d steps resolved: %d completed, %d failed, %d skipped)\n",
		d.Progress*100, d.Completed+d.Failed+d.Skipped, d.Total, d.Completed, d.Failed, d.Skipped)
	if d.InputTokens > 0 || d.OutputTokens > 0 {
		fmt.Fprintf(w, "tokens:   input=%d output=%d cost_usd=%.6f\n", d.InputTokens, d.OutputTokens, d.CostUSD)
	}
	if d.Run.InterruptionReason != "" {
		fmt.Fprintf(w, "interrupted: %s\n", d.Run.InterruptionReason)
	}
	fmt.Fprintln(w, "steps:")
	for _, st := range d.Run.Steps {
		line := fmt.Sprintf("  %-20s %-10s", st.ID, st.Status)
		if st.Error != "" {
			line += " error=" + truncateForTable(st.Error, 60)
		}
		fmt.Fprintln(w, line)
	}
}

func truncateForTable(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// workflowWaitDefaultTimeout mirrors runWaitForTaskDefaultTimeout's
// reasoning (task_outcome.go): generous enough for a real multi-step run,
// never an unbounded hang.
const workflowWaitDefaultTimeout = 30 * time.Minute

// workflowWaitPollInterval is deliberately coarser than
// runWaitForTaskPollInterval (500ms) — a workflow run's natural unit of
// progress is a whole step (seconds to minutes), not a single reasoning
// turn, so sub-second polling would just be waste.
const workflowWaitPollInterval = 2 * time.Second

// runWaitForWorkflow polls workflow.detail until the run reaches a terminal
// Status, printing the same live rollup a swarm run already emits on the
// event bus but with nowhere to be seen until now, then renders the final
// detail and returns a non-nil error for any non-Completed terminal status
// (mirrors runWaitForTask's governance-distinct-exit-code contract).
func runWaitForWorkflow(c taskStatusPoller, runID string, timeout time.Duration, sleep func(time.Duration)) error {
	if timeout <= 0 {
		timeout = workflowWaitDefaultTimeout
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	deadline := time.Now().Add(timeout)
	lastPrinted := ""
	for {
		var detail workflowui.Detail
		if err := c.Call("workflow.detail", map[string]any{"run_id": runID}, &detail); err != nil {
			return err
		}
		line := fmt.Sprintf("  %.0f%% (%d completed, %d failed, %d skipped / %d total)",
			detail.Progress*100, detail.Completed, detail.Failed, detail.Skipped, detail.Total)
		if line != lastPrinted {
			fmt.Println(line)
			lastPrinted = line
		}
		switch detail.Run.Status {
		case workflowui.Completed:
			renderWorkflowDetail(os.Stdout, detail)
			return nil
		case workflowui.Failed, workflowui.Stopped, workflowui.Interrupted:
			renderWorkflowDetail(os.Stdout, detail)
			reason := detail.Run.InterruptionReason
			if reason == "" {
				reason = string(detail.Run.Status)
			}
			return fmt.Errorf("workflow run %s: %s", runID, reason)
		}
		if time.Now().After(deadline) {
			renderWorkflowDetail(os.Stdout, detail)
			return fmt.Errorf("workflow run %s did not reach a terminal state within %s", runID, timeout)
		}
		sleep(workflowWaitPollInterval)
	}
}
