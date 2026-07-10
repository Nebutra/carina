package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type Schedule struct {
	ScheduleID string    `json:"schedule_id"`
	SessionID  string    `json:"session_id"`
	Prompt     string    `json:"prompt"`
	Kind       string    `json:"kind"` // at | every | cron
	Expression string    `json:"expression"`
	Enabled    bool      `json:"enabled"`
	NextRunAt  time.Time `json:"next_run_at"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	LastTaskID string    `json:"last_task_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ScheduleStore struct {
	mu   sync.Mutex
	path string
	rows map[string]*Schedule
}

func OpenScheduleStore(stateDir string) *ScheduleStore {
	s := &ScheduleStore{path: filepath.Join(stateDir, "schedules.json"), rows: map[string]*Schedule{}}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		var rows []*Schedule
		if json.Unmarshal(raw, &rows) == nil {
			for _, row := range rows {
				if row != nil && row.ScheduleID != "" {
					s.rows[row.ScheduleID] = row
				}
			}
		}
	}
	return s
}

func (s *ScheduleStore) Create(sessionID, prompt, kind, expression string, now time.Time) (*Schedule, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	expression = strings.TrimSpace(expression)
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("session_id and prompt are required")
	}
	next, err := nextScheduleTime(kind, expression, now)
	if err != nil {
		return nil, err
	}
	row := &Schedule{ScheduleID: sessionstore.NewID("sched"), SessionID: sessionID, Prompt: prompt, Kind: kind, Expression: expression, Enabled: true, NextRunAt: next, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	s.mu.Lock()
	s.rows[row.ScheduleID] = row
	err = s.persistLocked()
	s.mu.Unlock()
	return cloneSchedule(row), err
}

func (s *ScheduleStore) List() []*Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSortedSchedules(s.rows)
}

func (s *ScheduleStore) SetEnabled(id string, enabled bool, now time.Time) (*Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return nil, fmt.Errorf("unknown schedule %s", id)
	}
	row.Enabled = enabled
	row.UpdatedAt = now.UTC()
	if enabled && (row.NextRunAt.IsZero() || !row.NextRunAt.After(now)) {
		next, err := nextScheduleTime(row.Kind, row.Expression, now)
		if err != nil {
			return nil, err
		}
		row.NextRunAt = next
	}
	return cloneSchedule(row), s.persistLocked()
}

func (s *ScheduleStore) Delete(id string) (*Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return nil, fmt.Errorf("unknown schedule %s", id)
	}
	delete(s.rows, id)
	return cloneSchedule(row), s.persistLocked()
}

// ClaimDue advances due schedules before handing them to the daemon. This
// provides at-most-once triggering per store process; task execution remains
// observable and retryable through the normal scheduler/audit path.
func (s *ScheduleStore) ClaimDue(now time.Time) []*Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []*Schedule
	for _, row := range s.rows {
		if !row.Enabled || row.NextRunAt.IsZero() || row.NextRunAt.After(now) {
			continue
		}
		due = append(due, cloneSchedule(row))
		row.LastRunAt = now.UTC()
		row.UpdatedAt = now.UTC()
		if row.Kind == "at" {
			row.Enabled = false
			row.NextRunAt = time.Time{}
		} else if next, err := nextScheduleTime(row.Kind, row.Expression, now); err == nil {
			row.NextRunAt = next
		} else {
			row.Enabled = false
		}
	}
	if len(due) > 0 {
		_ = s.persistLocked()
	}
	return due
}

func (s *ScheduleStore) MarkTask(id, taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row := s.rows[id]; row != nil {
		row.LastTaskID = taskID
		row.UpdatedAt = time.Now().UTC()
		_ = s.persistLocked()
	}
}

func (s *ScheduleStore) RetryClaim(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row := s.rows[id]; row != nil {
		row.Enabled = true
		row.NextRunAt = now.Add(time.Minute).UTC()
		row.UpdatedAt = now.UTC()
		_ = s.persistLocked()
	}
}

func (s *ScheduleStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	rows := cloneSortedSchedules(s.rows)
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cloneSchedule(row *Schedule) *Schedule { cp := *row; return &cp }

func cloneSortedSchedules(rows map[string]*Schedule) []*Schedule {
	out := make([]*Schedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, cloneSchedule(row))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NextRunAt.Equal(out[j].NextRunAt) {
			return out[i].NextRunAt.Before(out[j].NextRunAt)
		}
		return out[i].ScheduleID < out[j].ScheduleID
	})
	return out
}

func nextScheduleTime(kind, expression string, now time.Time) (time.Time, error) {
	switch kind {
	case "at":
		at, err := time.Parse(time.RFC3339, expression)
		if err != nil || !at.After(now) {
			return time.Time{}, fmt.Errorf("at expression must be a future RFC3339 timestamp")
		}
		return at.UTC(), nil
	case "every":
		d, err := time.ParseDuration(expression)
		if err != nil || d < time.Second {
			return time.Time{}, fmt.Errorf("every expression must be a duration of at least 1s")
		}
		return now.Add(d).UTC(), nil
	case "cron":
		return nextCronTime(expression, now)
	default:
		return time.Time{}, fmt.Errorf("kind must be at, every, or cron")
	}
}

func nextCronTime(expression string, now time.Time) (time.Time, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron expression must have 5 fields: minute hour day month weekday")
	}
	for candidate := now.UTC().Truncate(time.Minute).Add(time.Minute); candidate.Before(now.AddDate(2, 0, 0)); candidate = candidate.Add(time.Minute) {
		values := []int{candidate.Minute(), candidate.Hour(), candidate.Day(), int(candidate.Month()), int(candidate.Weekday())}
		limits := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
		matches := make([]bool, len(fields))
		for i := range fields {
			ok, err := cronFieldMatches(fields[i], values[i], limits[i][0], limits[i][1])
			if err != nil {
				return time.Time{}, err
			}
			matches[i] = ok
		}
		dayMatch := matches[2] && matches[4]
		if fields[2] != "*" && fields[4] != "*" {
			dayMatch = matches[2] || matches[4]
		}
		matched := matches[0] && matches[1] && dayMatch && matches[3]
		if matched {
			return candidate, nil
		}
	}
	return time.Time{}, fmt.Errorf("cron expression has no run within two years")
}

func cronFieldMatches(field string, value, min, max int) (bool, error) {
	for _, part := range strings.Split(field, ",") {
		if part == "*" {
			return true, nil
		}
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(strings.TrimPrefix(part, "*/"))
			if err != nil || step <= 0 {
				return false, fmt.Errorf("invalid cron step %q", part)
			}
			if (value-min)%step == 0 {
				return true, nil
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < min || n > max {
			return false, fmt.Errorf("invalid cron field %q", field)
		}
		if n == value {
			return true, nil
		}
	}
	return false, nil
}
