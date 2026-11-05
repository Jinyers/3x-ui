package job

import (
	"context"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type PolicyRoutingJob struct {
	mu        sync.Mutex
	lastError string
}

func (j *PolicyRoutingJob) Run() {
	if !j.mu.TryLock() {
		return
	}
	defer j.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := (&service.PolicyRoutingService{}).Reconcile(ctx)
	next := ""
	if err != nil {
		next = err.Error()
	}
	if next != "" && next != j.lastError {
		logger.Warning("native policy routing:", next)
	}
	j.lastError = next
}
