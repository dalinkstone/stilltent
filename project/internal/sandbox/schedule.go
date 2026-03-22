package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ScheduleAction represents what action a schedule performs
type ScheduleAction string

const (
	ScheduleActionStart   ScheduleAction = "start"
	ScheduleActionStop    ScheduleAction = "stop"
	ScheduleActionRestart ScheduleAction = "restart"
	ScheduleActionPause   ScheduleAction = "pause"
	ScheduleActionUnpause ScheduleAction = "unpause"
)

// Schedule represents a scheduled action for a sandbox
type Schedule struct {
	ID        string         `json:"id"`
	Sandbox   string         `json:"sandbox"`
	Action    ScheduleAction `json:"action"`
	At        *time.Time     `json:"at,omitempty"`         // One-shot schedule
	Cron      string         `json:"cron,omitempty"`       // Recurring cron expression (simplified)
	Weekdays  []time.Weekday `json:"weekdays,omitempty"`   // Days of week for recurring
	TimeOfDay string         `json:"time_of_day,omitempty"` // HH:MM for recurring schedules
	Enabled   bool           `json:"enabled"`
	CreatedAt time.Time      `json:"created_at"`
	LastRun   *time.Time     `json:"last_run,omitempty"`
	NextRun   *time.Time     `json:"next_run,omitempty"`
}

// ScheduleManager handles persistent schedule storage and queries
type ScheduleManager struct {
	schedulePath string
	mu           sync.Mutex
}

// NewScheduleManager creates a new schedule manager for the given base directory
func NewScheduleManager(baseDir string) *ScheduleManager {
	return &ScheduleManager{
		schedulePath: filepath.Join(baseDir, "schedules.json"),
	}
}

// Add creates a new schedule and persists it
func (sm *ScheduleManager) Add(sched Schedule) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return err
	}

	// Check for duplicate ID
	for _, s := range schedules {
		if s.ID == sched.ID {
			return fmt.Errorf("schedule %q already exists", sched.ID)
		}
	}

	sched.CreatedAt = time.Now().UTC()
	sched.Enabled = true

	// Compute next run
	next := sm.computeNextRun(&sched)
	sched.NextRun = next

	schedules = append(schedules, sched)
	return sm.saveLocked(schedules)
}

// Remove deletes a schedule by ID
func (sm *ScheduleManager) Remove(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return err
	}

	found := false
	var result []Schedule
	for _, s := range schedules {
		if s.ID == id {
			found = true
			continue
		}
		result = append(result, s)
	}

	if !found {
		return fmt.Errorf("schedule %q not found", id)
	}

	return sm.saveLocked(result)
}

// Enable enables or disables a schedule
func (sm *ScheduleManager) Enable(id string, enabled bool) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return err
	}

	found := false
	for i := range schedules {
		if schedules[i].ID == id {
			schedules[i].Enabled = enabled
			if enabled {
				next := sm.computeNextRun(&schedules[i])
				schedules[i].NextRun = next
			} else {
				schedules[i].NextRun = nil
			}
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("schedule %q not found", id)
	}

	return sm.saveLocked(schedules)
}

// List returns all schedules, optionally filtered by sandbox name
func (sm *ScheduleManager) List(sandbox string) ([]Schedule, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return nil, err
	}

	if sandbox == "" {
		return schedules, nil
	}

	var filtered []Schedule
	for _, s := range schedules {
		if s.Sandbox == sandbox {
			filtered = append(filtered, s)
		}
	}
	return filtered, nil
}

// Get returns a specific schedule by ID
func (sm *ScheduleManager) Get(id string) (*Schedule, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return nil, err
	}

	for _, s := range schedules {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("schedule %q not found", id)
}

// GetDue returns all schedules that are due to run (NextRun <= now and enabled)
func (sm *ScheduleManager) GetDue() ([]Schedule, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var due []Schedule
	for _, s := range schedules {
		if s.Enabled && s.NextRun != nil && !s.NextRun.After(now) {
			due = append(due, s)
		}
	}
	return due, nil
}

// MarkRun updates a schedule's last run time and computes the next run
func (sm *ScheduleManager) MarkRun(id string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return err
	}

	for i := range schedules {
		if schedules[i].ID == id {
			now := time.Now().UTC()
			schedules[i].LastRun = &now

			// One-shot schedules: disable after run
			if schedules[i].At != nil && schedules[i].TimeOfDay == "" {
				schedules[i].Enabled = false
				schedules[i].NextRun = nil
			} else {
				next := sm.computeNextRun(&schedules[i])
				schedules[i].NextRun = next
			}
			return sm.saveLocked(schedules)
		}
	}

	return fmt.Errorf("schedule %q not found", id)
}

// RemoveBySandbox removes all schedules for a given sandbox
func (sm *ScheduleManager) RemoveBySandbox(sandbox string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	schedules, err := sm.loadLocked()
	if err != nil {
		return err
	}

	var result []Schedule
	for _, s := range schedules {
		if s.Sandbox != sandbox {
			result = append(result, s)
		}
	}

	return sm.saveLocked(result)
}

// computeNextRun calculates the next run time for a schedule
func (sm *ScheduleManager) computeNextRun(sched *Schedule) *time.Time {
	now := time.Now().UTC()

	// One-shot schedule
	if sched.At != nil && sched.TimeOfDay == "" {
		if sched.At.After(now) {
			t := *sched.At
			return &t
		}
		return nil
	}

	// Recurring schedule with time of day and optional weekdays
	if sched.TimeOfDay != "" {
		var hour, min int
		if _, err := fmt.Sscanf(sched.TimeOfDay, "%d:%d", &hour, &min); err != nil {
			return nil
		}

		// Find the next occurrence
		candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, time.UTC)

		// If we already passed today's time, start from tomorrow
		if !candidate.After(now) {
			candidate = candidate.Add(24 * time.Hour)
		}

		// If weekdays are specified, find the next matching day
		if len(sched.Weekdays) > 0 {
			for i := 0; i < 8; i++ {
				wd := candidate.Weekday()
				for _, allowed := range sched.Weekdays {
					if wd == allowed {
						return &candidate
					}
				}
				candidate = candidate.Add(24 * time.Hour)
			}
			return nil // No matching weekday found (shouldn't happen)
		}

		return &candidate
	}

	return nil
}

func (sm *ScheduleManager) loadLocked() ([]Schedule, error) {
	data, err := os.ReadFile(sm.schedulePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read schedules: %w", err)
	}

	var schedules []Schedule
	if err := json.Unmarshal(data, &schedules); err != nil {
		return nil, fmt.Errorf("failed to parse schedules: %w", err)
	}
	return schedules, nil
}

func (sm *ScheduleManager) saveLocked(schedules []Schedule) error {
	if err := os.MkdirAll(filepath.Dir(sm.schedulePath), 0755); err != nil {
		return fmt.Errorf("failed to create schedule directory: %w", err)
	}

	data, err := json.MarshalIndent(schedules, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal schedules: %w", err)
	}

	return os.WriteFile(sm.schedulePath, data, 0644)
}
