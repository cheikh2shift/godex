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

func (s *schedulerProviderGetter) GetProvider(cfg interface{}) (interface{}, error) {
	cfgMap, ok := cfg.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid config type")
	}

	providerType, _ := cfgMap["type"].(string)
	providerName, _ := cfgMap["name"].(string)
	providerModel, _ := cfgMap["model"].(string)

	if providerType == "unknown" && s.provider != nil {
		providerType = s.provider.Type
		providerName = s.provider.Name
		providerModel = s.provider.Model
	}

	return agent.GetProviderFromConfig(&agent.ProviderConfig{
		Type:  providerType,
		Name:  providerName,
		Model: providerModel,
	})
}

func handleSchedule(input string) {

	if sched == nil {
		fmt.Println("[Scheduler] Not initialized")
		return
	}

	parts := strings.Fields(input)
	if len(parts) == 1 {
		listScheduledTasks()
		return
	}

	if len(parts) >= 2 && (parts[1] == "help" || parts[1] == "-h" || parts[1] == "--help") {
		fmt.Println("Scheduler - Schedule prompts to run at specific times or intervals")
		fmt.Println("")
		fmt.Println("Usage:")
		fmt.Println("  /schedule              - List all scheduled tasks")
		fmt.Println("  /schedule stop <id>    - Stop a running task by 4-char ID")
		fmt.Println("  /schedule remove <id> - Remove a task from the schedule by 4-char ID")
		fmt.Println("  /schedule help        - Show this help message")
		fmt.Println("")
		fmt.Println("LLM Tool (schedule_task):")
		fmt.Println("  prompt: The prompt to execute")
		fmt.Println("  interval_sec: Repeating interval in seconds (e.g., 60, 3600)")
		fmt.Println("  run_at: Time in HH:MM format (e.g., '14:30') or 'now' for immediate execution")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  schedule_task(prompt='Summarize my emails', interval_sec=3600)")
		fmt.Println("  schedule_task(prompt='Check system status', run_at='09:00')")
		fmt.Println("  schedule_task(prompt='Generate report', run_at='now')")
		return
	}

	if len(parts) >= 3 && parts[1] == "stop" {
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

	fmt.Println("Usage:")
	fmt.Println("  /schedule              - List all scheduled tasks")
	fmt.Println("  /schedule stop <id>    - Stop a task by 4-char ID")
	fmt.Println("  /schedule remove <id> - Remove a task by 4-char ID")
}

func listScheduledTasks() {
	tasks := sched.ListTasks()
	if len(tasks) == 0 {
		fmt.Println("No scheduled tasks")
		return
	}

	fmt.Printf("\n%-6s %-12s %-10s %-8s %-8s %s\n", "ID", "Created", "Interval", "Runs", "Last Run", "Status")
	fmt.Println(strings.Repeat("-", 80))

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
			lastRun = task.LastRun.Format("2006-01-02 15:04")
		}

		status := "OK"
		if task.LastError != "" {
			status = truncate(task.LastError, 30)
		}

		fmt.Printf("%-6s %-12s %-10s %-8d %-8s %s\n",
			task.ID,
			task.CreatedAt.Format("2006-01-02"),
			interval,
			task.RunCount,
			lastRun,
			status,
		)
	}
	fmt.Println()
}
