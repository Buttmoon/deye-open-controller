package main

import "time"

// startBackgroundServices runs low-priority maintenance: history retention,
// monthly regeneration of recurring schedules and the optional status poller.
// None of these jobs writes to an inverter.
func (a *App) startBackgroundServices() {
	if a.backgroundStopCh != nil {
		return
	}
	stop := make(chan struct{})
	a.backgroundStopCh = stop

	a.backgroundWG.Add(1)
	go func() {
		defer a.backgroundWG.Done()
		a.runMaintenance()
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				a.runMaintenance()
			}
		}
	}()

	a.backgroundWG.Add(1)
	go func() {
		defer a.backgroundWG.Done()
		a.status.run(stop)
	}()
}

func (a *App) stopBackgroundServices() {
	if a.backgroundStopCh == nil {
		return
	}
	close(a.backgroundStopCh)
	a.backgroundWG.Wait()
	a.backgroundStopCh = nil
}

func (a *App) runMaintenance() {
	if n, err := a.trimHistory(); err != nil {
		a.appendAppLog("warn", "history retention failed", map[string]any{"component": "history", "error": err.Error()})
	} else if n > 0 {
		a.appendAppLog("info", "history retention removed rows", map[string]any{"component": "history", "rows": n})
	}
	a.renewRecurringSchedules(time.Now())
}
