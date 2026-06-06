package main

import (
	"fmt"
	"strings"

	"github.com/cheikh2shift/godex/internal/agent"
	"github.com/cheikh2shift/godex/internal/config"
	"github.com/cheikh2shift/godex/internal/scheduler"
)

type schedulerProviderGetter struct {
	provider *config.Provider
}

func (s *schedulerProviderGetter) GetProvider(cfg any) (any, error) {
	cfgMap, ok := cfg.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid config type")
	}

	providerType, _ := cfgMap["type"].(string)
	providerName, _ := cfgMap["name"].(string)
	providerModel, _ := cfgMap["model"].(string)

	// The scheduler often requests a "current" provider. For providers like OpenRouter,
	// the API key lives on the full config.Provider struct, so we must pass it through.
	useCurrent := s.provider != nil && (providerType == "" || providerType == "unknown" || providerType == "current" || providerName == "" || providerName == "current")
	if useCurrent {
		// Reinitialize to ensure runtime auth (OpenRouter keys, env) is present for scheduler runs.
		// NOTE: We intentionally do not override the model here to avoid mutating global config.
		_ = providerModel
		return agent.ReinitializeProvider(s.provider)
	}

	return agent.GetProviderFromConfig(&agent.ProviderConfig{
		Type:  providerType,
		Name:  providerName,
		Model: providerModel,
	})
}

func handleSchedule(input string) {
	parts := strings.Fields(input)
	if len(parts) >= 2 && (parts[1] == "help" || parts[1] == "-h" || parts[1] == "--help") {
		fmt.Println("Scheduler - Schedule prompts to run at specific times or intervals")
		fmt.Println("")
		fmt.Println("Usage:")
		fmt.Println("  /schedule              - List all scheduled tasks")
		fmt.Println("  /schedule <id>         - Show last output of a task")
		fmt.Println("  /schedule stop <id>    - Stop a running task by 4-char ID")
		fmt.Println("  /schedule stop --all   - Stop all running tasks")
		fmt.Println("  /schedule remove <id>  - Remove a task from the schedule by 4-char ID")
		fmt.Println("  /schedule remove --all - Remove all tasks from the schedule")
		fmt.Println("  /schedule clear        - Stop and remove all tasks")
		fmt.Println("  /schedule help         - Show this help message")
		fmt.Println("")
		fmt.Println("LLM Tool (scheduler):")
		fmt.Println("  prompt: The prompt to execute")
		fmt.Println("  interval_sec: Repeating interval in seconds (e.g., 60, 3600)")
		fmt.Println("  run_at: Time in HH:MM format (e.g., '14:30') or 'now' for immediate execution")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  scheduler(prompt='Summarize my emails', interval_sec=3600)")
		fmt.Println("  scheduler(prompt='Check system status', run_at='09:00')")
		fmt.Println("  scheduler(prompt='Generate report', run_at='now')")
		return
	}

	if sched == nil {
		fmt.Println("[Scheduler] Not initialized")
		return
	}

	if len(parts) == 1 {
		listScheduledTasks()
		return
	}

	if len(parts) == 2 && (parts[1] == "clear" || parts[1] == "clear-all") {
		tasks := sched.ListTasks()
		if len(tasks) == 0 {
			fmt.Println("No scheduled tasks")
			return
		}
		sched.StopAllTasks()
		removed := 0
		for _, t := range tasks {
			task, ok := t.(*scheduler.ScheduledTask)
			if !ok || task == nil {
				continue
			}
			if sched.RemoveTask(task.ID) {
				removed++
			}
		}
		fmt.Printf("Cleared %d task(s)\n", removed)
		return
	}

	if len(parts) >= 3 && parts[1] == "stop" {
		if parts[2] == "all" || parts[2] == "--all" || parts[2] == "*" {
			sched.StopAllTasks()
			fmt.Println("Stopped all running tasks")
			return
		}
		id := strings.ToUpper(parts[2])
		if len(id) != 4 {
			fmt.Println("Invalid task ID. Use 4-character identifier (e.g., ABC1)")
			return
		}
		if sched.StopTask(id) {
			fmt.Printf("Task %s stopped\n", id)
		} else {
			fmt.Printf("Task %s not found\n", id)
		}
		return
	}

	if len(parts) >= 3 && parts[1] == "remove" {
		if parts[2] == "all" || parts[2] == "--all" || parts[2] == "*" {
			tasks := sched.ListTasks()
			if len(tasks) == 0 {
				fmt.Println("No scheduled tasks")
				return
			}
			removed := 0
			for _, t := range tasks {
				task, ok := t.(*scheduler.ScheduledTask)
				if !ok || task == nil {
					continue
				}
				if sched.RemoveTask(task.ID) {
					removed++
				}
			}
			fmt.Printf("Removed %d task(s)\n", removed)
			return
		}
		id := strings.ToUpper(parts[2])
		if len(id) != 4 {
			fmt.Println("Invalid task ID. Use 4-character identifier (e.g., ABC1)")
			return
		}
		if sched.RemoveTask(id) {
			fmt.Printf("Task %s removed\n", id)
		} else {
			fmt.Printf("Task %s not found\n", id)
		}
		return
	}

	if len(parts) == 2 && len(parts[1]) == 4 {
		id := strings.ToUpper(parts[1])
		task := sched.GetTask(id)
		if task == nil {
			fmt.Printf("Task %s not found\n", id)
			return
		}
		st, ok := task.(*scheduler.ScheduledTask)
		if !ok || st == nil {
			fmt.Printf("Task %s not found\n", id)
			return
		}
		if st.LastOutput == "" {

			if st.LastError != "" {
				fmt.Printf("Error for task %s:\n%s\n", id, st.LastError)
				return
			}
			fmt.Printf("No output for task %s\n", id)
			return
		}
		fmt.Printf("Output for task %s:\n%s\n", id, renderMarkdown(st.LastOutput))
		return
	}

	fmt.Println("Usage:")
	fmt.Println("  /schedule              - List all scheduled tasks")
	fmt.Println("  /schedule <id>         - Show last output of a task")
	fmt.Println("  /schedule stop <id>    - Stop a task by 4-char ID")
	fmt.Println("  /schedule remove <id>  - Remove a task by 4-char ID")
	fmt.Println("  /schedule clear        - Stop and remove all tasks")
}

func listScheduledTasks() {
	tasks := sched.ListTasks()
	if len(tasks) == 0 {
		fmt.Println("No scheduled tasks")
		return
	}

	fmt.Printf("\n%-6s %-8s %-8s %-8s %s\n", "ID", "Runs", "Interval", "Last Run", "Output")
	fmt.Println(strings.Repeat("-", 70))

	for _, t := range tasks {
		task, ok := t.(*scheduler.ScheduledTask)
		if !ok {
			continue
		}

		interval := fmt.Sprintf("%ds", task.IntervalSec)
		if task.RunAt != "" {
			interval = task.RunAt
		}

		lastRun := "Never"
		if !task.LastRun.IsZero() {
			lastRun = task.LastRun.Format("15:04")
		}

		output := "OK"
		if task.LastError != "" {
			output = truncate(task.LastError, 40)
		} else if task.LastOutput != "" {
			lines := strings.Split(task.LastOutput, "\n")
			lastLine := strings.TrimSpace(lines[len(lines)-1])
			output = truncate(lastLine, 40)
		}

		fmt.Printf("%-6s %-8d %-8s %-8s %s\n",
			task.ID,
			task.RunCount,
			interval,
			lastRun,
			output,
		)
	}
	fmt.Println()
}
