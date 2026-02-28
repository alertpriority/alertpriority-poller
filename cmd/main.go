package main

import (
	"appoller/checker"
	"appoller/client"
	"appoller/config"
	"appoller/health"
	"appoller/scheduler"
	"flag"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Set via -ldflags at build time
var (
	version   = "1.0.0"
	gitHash   = "unknown"
	gitBranch = "unknown"
	buildTime = "unknown"
)

func main() {
	configFile := flag.String("config", "", "path to config file (optional)")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("[main] configuration error: %v", err)
	}

	hostname, _ := os.Hostname()
	log.Printf("[main] AlertPriority Poller v%s starting on %s", version, hostname)
	log.Printf("[main] API URL: %s", cfg.APIURL)
	log.Printf("[main] poll_interval=%ds max_concurrency=%d batch_size=%d batch_interval=%ds",
		cfg.PollInterval, cfg.MaxConcurrency, cfg.BatchSize, cfg.BatchInterval)

	// Initialize API client
	apiClient := client.NewClient(cfg)

	// Register with API
	log.Printf("[main] registering with API...")
	regResp, err := apiClient.Register(hostname, version)
	if err != nil {
		log.Fatalf("[main] failed to register: %v", err)
	}
	log.Printf("[main] registered as poller %s at location %s (%s)",
		regResp.PollerUUID, regResp.LocationName, regResp.LocationKey)

	pollerUUID := regResp.PollerUUID

	// Start health server
	healthServer := health.NewServer(cfg.HealthPort)
	healthServer.Start()

	// Initialize scheduler
	sched := scheduler.NewScheduler()

	// Result buffer for batch submission
	var resultMu sync.Mutex
	resultBuffer := make([]client.CheckResult, 0, cfg.BatchSize)

	// Signal handling
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})

	// Worker pool for executing checks
	checkChan := make(chan *client.MonitorAssignment, cfg.MaxConcurrency*2)
	var wg sync.WaitGroup

	for i := 0; i < cfg.MaxConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range checkChan {
				result := checker.Execute(m, cfg.TLSInsecure)
				healthServer.ChecksExecuted.Add(1)
				if !result.Success {
					healthServer.Errors.Add(1)
				}

				cr := result.ToClientResult(pollerUUID)
				resultMu.Lock()
				resultBuffer = append(resultBuffer, cr)
				resultMu.Unlock()
			}
		}()
	}

	// Monitor fetch loop
	go func() {
		// Initial fetch immediately
		monitors, err := apiClient.GetMonitors()
		if err != nil {
			log.Printf("[main] initial monitor fetch failed: %v", err)
		} else {
			sched.UpdateMonitors(monitors)
			log.Printf("[main] loaded %d monitors", len(monitors))
		}

		ticker := time.NewTicker(time.Duration(cfg.PollInterval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				monitors, err := apiClient.GetMonitors()
				if err != nil {
					log.Printf("[main] monitor fetch failed: %v", err)
					continue
				}
				sched.UpdateMonitors(monitors)
				log.Printf("[main] refreshed %d monitors", len(monitors))
			}
		}
	}()

	// Check executor loop — polls scheduler every second for due checks
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				dueChecks := sched.GetDueChecks(cfg.MaxConcurrency)
				healthServer.QueueDepth.Store(int64(len(dueChecks)))
				for _, m := range dueChecks {
					select {
					case checkChan <- m:
					default:
						log.Printf("[main] check channel full, dropping check for %s", m.UUID)
					}
				}
			}
		}
	}()

	// Result submitter loop
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.BatchInterval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				resultMu.Lock()
				if len(resultBuffer) == 0 {
					resultMu.Unlock()
					continue
				}
				batch := make([]client.CheckResult, len(resultBuffer))
				copy(batch, resultBuffer)
				resultBuffer = resultBuffer[:0]
				resultMu.Unlock()

				resp, err := apiClient.SubmitResults(pollerUUID, batch)
				if err != nil {
					log.Printf("[main] failed to submit %d results: %v", len(batch), err)
					// Re-add to buffer on failure
					resultMu.Lock()
					resultBuffer = append(batch, resultBuffer...)
					resultMu.Unlock()
				} else {
					log.Printf("[main] submitted %d results (accepted: %d, rejected: %d)",
						len(batch), resp.Accepted, resp.Rejected)
				}
			}
		}
	}()

	// Heartbeat loop
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				status := "online"
				resultMu.Lock()
				queueSize := len(resultBuffer)
				resultMu.Unlock()
				if queueSize > cfg.BatchSize {
					status = "busy"
				}

				err := apiClient.Heartbeat(&client.HeartbeatRequest{
					PollerUUID:         pollerUUID,
					Status:             status,
					ChecksExecuted:     healthServer.ChecksExecuted.Load(),
					ChecksPerMinute:    float64(healthServer.ChecksPerMinute.Load()),
					AvgCheckDurationMs: healthServer.AvgCheckDurationMs.Load(),
					Errors:             healthServer.Errors.Load(),
					UptimeSeconds:      healthServer.UptimeSeconds(),
					QueueDepth:         queueSize,
					Version:            version,
				})
				if err != nil {
					log.Printf("[main] heartbeat failed: %v", err)
				}
			}
		}
	}()

	// Mark as ready
	healthServer.SetReady(true)
	log.Printf("[main] poller is ready, health endpoint on :%d", cfg.HealthPort)

	// Wait for shutdown signal
	<-signals
	log.Printf("[main] received shutdown signal, draining...")

	// Graceful shutdown
	close(done)
	close(checkChan)

	// Wait for in-flight checks (max 30s)
	shutdownDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
		log.Printf("[main] all checks completed")
	case <-time.After(30 * time.Second):
		log.Printf("[main] shutdown timeout, some checks may not have completed")
	}

	// Flush remaining results
	resultMu.Lock()
	if len(resultBuffer) > 0 {
		log.Printf("[main] flushing %d remaining results", len(resultBuffer))
		_, err := apiClient.SubmitResults(pollerUUID, resultBuffer)
		if err != nil {
			log.Printf("[main] failed to flush results: %v", err)
		}
	}
	resultMu.Unlock()

	// Send final shutting_down heartbeat
	_ = apiClient.Heartbeat(&client.HeartbeatRequest{
		PollerUUID:    pollerUUID,
		Status:        "shutting_down",
		UptimeSeconds: healthServer.UptimeSeconds(),
		Version:       version,
	})

	log.Printf("[main] poller shut down gracefully")
}
