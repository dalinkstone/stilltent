package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func scheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage scheduled actions for sandboxes",
		Long: `Schedule automatic start, stop, restart, pause, or unpause actions for sandboxes.

Schedules can be one-shot (at a specific time) or recurring (daily or on specific weekdays).

Examples:
  tent schedule add mybox-start --sandbox mybox --action start --time 08:00 --weekdays mon,tue,wed,thu,fri
  tent schedule add mybox-stop --sandbox mybox --action stop --time 18:00 --weekdays mon,tue,wed,thu,fri
  tent schedule add mybox-once --sandbox mybox --action start --at "2026-03-23T10:00:00Z"
  tent schedule list
  tent schedule remove mybox-start
  tent schedule enable mybox-stop
  tent schedule disable mybox-stop`,
	}

	cmd.AddCommand(scheduleAddCmd())
	cmd.AddCommand(scheduleRemoveCmd())
	cmd.AddCommand(scheduleListCmd())
	cmd.AddCommand(scheduleEnableCmd())
	cmd.AddCommand(scheduleDisableCmd())
	cmd.AddCommand(scheduleDueCmd())

	return cmd
}

func scheduleAddCmd() *cobra.Command {
	var (
		sandbox  string
		action   string
		atTime   string
		timeStr  string
		weekdays string
	)

	cmd := &cobra.Command{
		Use:   "add <schedule-id>",
		Short: "Add a new schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			if sandbox == "" {
				return fmt.Errorf("--sandbox is required")
			}
			if action == "" {
				return fmt.Errorf("--action is required")
			}

			schedAction := vm.ScheduleAction(action)
			switch schedAction {
			case vm.ScheduleActionStart, vm.ScheduleActionStop, vm.ScheduleActionRestart,
				vm.ScheduleActionPause, vm.ScheduleActionUnpause:
				// valid
			default:
				return fmt.Errorf("invalid action %q: must be start, stop, restart, pause, or unpause", action)
			}

			sched := vm.Schedule{
				ID:      id,
				Sandbox: sandbox,
				Action:  schedAction,
			}

			if atTime != "" && timeStr != "" {
				return fmt.Errorf("cannot specify both --at and --time; use --at for one-shot, --time for recurring")
			}

			if atTime != "" {
				t, err := time.Parse(time.RFC3339, atTime)
				if err != nil {
					return fmt.Errorf("invalid --at time %q: use RFC3339 format (e.g. 2026-03-23T10:00:00Z): %w", atTime, err)
				}
				sched.At = &t
			}

			if timeStr != "" {
				// Validate HH:MM format
				var h, m int
				if _, err := fmt.Sscanf(timeStr, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
					return fmt.Errorf("invalid --time %q: use HH:MM format (e.g. 08:00, 18:30)", timeStr)
				}
				sched.TimeOfDay = timeStr
			}

			if weekdays != "" {
				wds, err := parseWeekdays(weekdays)
				if err != nil {
					return err
				}
				sched.Weekdays = wds
			}

			if atTime == "" && timeStr == "" {
				return fmt.Errorf("must specify either --at (one-shot) or --time (recurring)")
			}

			baseDir := getBaseDir()
			mgr := vm.NewScheduleManager(baseDir)
			if err := mgr.Add(sched); err != nil {
				return err
			}

			// Retrieve to show computed next run
			saved, err := mgr.Get(id)
			if err != nil {
				return err
			}

			fmt.Printf("Schedule %q created for sandbox %q\n", id, sandbox)
			fmt.Printf("  Action:   %s\n", saved.Action)
			if saved.At != nil && saved.TimeOfDay == "" {
				fmt.Printf("  At:       %s\n", saved.At.Format(time.RFC3339))
			}
			if saved.TimeOfDay != "" {
				fmt.Printf("  Time:     %s UTC\n", saved.TimeOfDay)
			}
			if len(saved.Weekdays) > 0 {
				fmt.Printf("  Weekdays: %s\n", formatWeekdays(saved.Weekdays))
			}
			if saved.NextRun != nil {
				fmt.Printf("  Next run: %s\n", saved.NextRun.Format(time.RFC3339))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&sandbox, "sandbox", "", "Sandbox name (required)")
	cmd.Flags().StringVar(&action, "action", "", "Action to perform: start, stop, restart, pause, unpause (required)")
	cmd.Flags().StringVar(&atTime, "at", "", "One-shot schedule time in RFC3339 format")
	cmd.Flags().StringVar(&timeStr, "time", "", "Recurring daily time in HH:MM format (UTC)")
	cmd.Flags().StringVar(&weekdays, "weekdays", "", "Comma-separated weekdays for recurring (mon,tue,wed,thu,fri,sat,sun)")

	return cmd
}

func scheduleRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <schedule-id>",
		Short: "Remove a schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewScheduleManager(baseDir)
			if err := mgr.Remove(args[0]); err != nil {
				return err
			}
			fmt.Printf("Schedule %q removed.\n", args[0])
			return nil
		},
	}
}

func scheduleListCmd() *cobra.Command {
	var (
		sandbox    string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all schedules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewScheduleManager(baseDir)
			schedules, err := mgr.List(sandbox)
			if err != nil {
				return err
			}

			if len(schedules) == 0 {
				fmt.Println("No schedules found.")
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(schedules)
			}

			fmt.Printf("%-20s %-15s %-10s %-8s %-10s %-25s %-25s\n",
				"ID", "SANDBOX", "ACTION", "ENABLED", "TIME", "NEXT RUN", "LAST RUN")

			for _, s := range schedules {
				enabled := "yes"
				if !s.Enabled {
					enabled = "no"
				}

				timeCol := "-"
				if s.At != nil && s.TimeOfDay == "" {
					timeCol = s.At.Format("2006-01-02 15:04")
				} else if s.TimeOfDay != "" {
					timeCol = s.TimeOfDay
					if len(s.Weekdays) > 0 {
						timeCol += " " + formatWeekdays(s.Weekdays)
					}
				}

				nextRun := "-"
				if s.NextRun != nil {
					nextRun = s.NextRun.Format("2006-01-02 15:04")
				}

				lastRun := "-"
				if s.LastRun != nil {
					lastRun = s.LastRun.Format("2006-01-02 15:04")
				}

				fmt.Printf("%-20s %-15s %-10s %-8s %-10s %-25s %-25s\n",
					s.ID, s.Sandbox, s.Action, enabled, timeCol, nextRun, lastRun)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&sandbox, "sandbox", "", "Filter by sandbox name")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func scheduleEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <schedule-id>",
		Short: "Enable a schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewScheduleManager(baseDir)
			if err := mgr.Enable(args[0], true); err != nil {
				return err
			}
			fmt.Printf("Schedule %q enabled.\n", args[0])
			return nil
		},
	}
}

func scheduleDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <schedule-id>",
		Short: "Disable a schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewScheduleManager(baseDir)
			if err := mgr.Enable(args[0], false); err != nil {
				return err
			}
			fmt.Printf("Schedule %q disabled.\n", args[0])
			return nil
		},
	}
}

func scheduleDueCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "due",
		Short: "Show schedules that are due to run",
		Long:  `List all enabled schedules whose next run time has passed. Used by the scheduler daemon to determine which actions to execute.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			mgr := vm.NewScheduleManager(baseDir)
			due, err := mgr.GetDue()
			if err != nil {
				return err
			}

			if len(due) == 0 {
				fmt.Println("No schedules are due.")
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(due)
			}

			for _, s := range due {
				nextStr := "-"
				if s.NextRun != nil {
					nextStr = s.NextRun.Format(time.RFC3339)
				}
				fmt.Printf("%-20s %-15s %-10s  (due since %s)\n", s.ID, s.Sandbox, s.Action, nextStr)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

// parseWeekdays parses a comma-separated weekday string
func parseWeekdays(s string) ([]time.Weekday, error) {
	parts := strings.Split(strings.ToLower(s), ",")
	var result []time.Weekday
	seen := make(map[time.Weekday]bool)

	for _, p := range parts {
		p = strings.TrimSpace(p)
		var wd time.Weekday
		switch p {
		case "sun", "sunday":
			wd = time.Sunday
		case "mon", "monday":
			wd = time.Monday
		case "tue", "tuesday":
			wd = time.Tuesday
		case "wed", "wednesday":
			wd = time.Wednesday
		case "thu", "thursday":
			wd = time.Thursday
		case "fri", "friday":
			wd = time.Friday
		case "sat", "saturday":
			wd = time.Saturday
		default:
			return nil, fmt.Errorf("invalid weekday %q: use mon,tue,wed,thu,fri,sat,sun", p)
		}
		if !seen[wd] {
			result = append(result, wd)
			seen[wd] = true
		}
	}

	return result, nil
}

// formatWeekdays formats weekdays as a short comma-separated string
func formatWeekdays(wds []time.Weekday) string {
	names := make([]string, len(wds))
	for i, wd := range wds {
		switch wd {
		case time.Sunday:
			names[i] = "Sun"
		case time.Monday:
			names[i] = "Mon"
		case time.Tuesday:
			names[i] = "Tue"
		case time.Wednesday:
			names[i] = "Wed"
		case time.Thursday:
			names[i] = "Thu"
		case time.Friday:
			names[i] = "Fri"
		case time.Saturday:
			names[i] = "Sat"
		}
	}
	return strings.Join(names, ",")
}
