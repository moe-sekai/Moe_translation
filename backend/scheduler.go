package backend

import (
	"fmt"
	"sync"
	"time"
)

type SchedulerStatus struct {
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	NextRun   string `json:"nextRun,omitempty"`
	LastRun   string `json:"lastRun,omitempty"`
	LastError string `json:"lastError,omitempty"`
}

type Scheduler struct {
	translator *Translator
	pusher     *Pusher
	store      *Store
	hourUTC    int
	enabled    bool

	mu     sync.Mutex
	status SchedulerStatus
}

func NewScheduler(translator *Translator, pusher *Pusher, store *Store, hourUTC int, enabled bool) *Scheduler {
	if hourUTC < 0 || hourUTC > 23 {
		hourUTC = 4
	}
	s := &Scheduler{
		translator: translator,
		pusher:     pusher,
		store:      store,
		hourUTC:    hourUTC,
		enabled:    enabled,
	}
	s.status.Enabled = enabled
	return s
}

func (s *Scheduler) Status() SchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Scheduler) Start() {
	if !s.enabled {
		return
	}
	go func() {
		for {
			next := nextRunUTC(s.hourUTC, time.Now())
			s.mu.Lock()
			s.status.NextRun = next.Format(time.RFC3339)
			s.mu.Unlock()

			wait := time.Until(next)
			if wait < 0 {
				wait = time.Second
			}
			timer := time.NewTimer(wait)
			<-timer.C
			_ = s.RunOnce("scheduled")
		}
	}()
}

func (s *Scheduler) RunOnce(trigger string) error {
	s.mu.Lock()
	if s.status.Running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler job already running")
	}
	s.status.Running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.status.Running = false
		s.status.LastRun = time.Now().UTC().Format(time.RFC3339)
		s.mu.Unlock()
	}()

	_, err := s.translator.SyncCNOnly()
	if err != nil {
		s.mu.Lock()
		s.status.LastError = err.Error()
		s.mu.Unlock()
		return err
	}

	if err := s.pusher.PushAll(s.store, trigger); err != nil {
		s.mu.Lock()
		s.status.LastError = err.Error()
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.status.LastError = ""
	s.mu.Unlock()
	return nil
}

func nextRunUTC(hour int, now time.Time) time.Time {
	utc := now.UTC()
	run := time.Date(utc.Year(), utc.Month(), utc.Day(), hour, 0, 0, 0, time.UTC)
	if !utc.Before(run) {
		run = run.Add(24 * time.Hour)
	}
	return run
}
