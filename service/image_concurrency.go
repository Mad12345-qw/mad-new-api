package service

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrImageQueueFull     = errors.New("image generation queue is full")
	ErrImageQueueTimedOut = errors.New("image generation queue timed out")
)

type imageConcurrencySettings struct {
	globalLimit   int
	editLimit     int
	familyLimits  map[string]int
	queueCapacity int
	queueTimeout  time.Duration
	retryAfter    int
}

type imageConcurrencyWaiter struct {
	family string
	isEdit bool
}

type imageConcurrencyController struct {
	mu           sync.Mutex
	settings     imageConcurrencySettings
	activeTotal  int
	activeEdits  int
	activeFamily map[string]int
	waiters      []*imageConcurrencyWaiter
	changed      chan struct{}
}

func imageConcurrencyEnv(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func loadImageConcurrencySettings() imageConcurrencySettings {
	return imageConcurrencySettings{
		globalLimit: imageConcurrencyEnv("MADAPI_IMAGE_GLOBAL_CONCURRENCY", 100),
		editLimit:   imageConcurrencyEnv("MADAPI_IMAGE_EDIT_CONCURRENCY", 30),
		familyLimits: map[string]int{
			"image2": imageConcurrencyEnv("MADAPI_IMAGE_FAMILY_IMAGE2_CONCURRENCY", 100),
			"gemini": imageConcurrencyEnv("MADAPI_IMAGE_FAMILY_GEMINI_CONCURRENCY", 30),
			"grok":   imageConcurrencyEnv("MADAPI_IMAGE_FAMILY_GROK_CONCURRENCY", 30),
			"other":  imageConcurrencyEnv("MADAPI_IMAGE_FAMILY_OTHER_CONCURRENCY", 30),
		},
		queueCapacity: imageConcurrencyEnv("MADAPI_IMAGE_QUEUE_CAPACITY", 20),
		queueTimeout:  time.Duration(imageConcurrencyEnv("MADAPI_IMAGE_QUEUE_TIMEOUT_SECONDS", 45)) * time.Second,
		retryAfter:    imageConcurrencyEnv("MADAPI_IMAGE_QUEUE_RETRY_AFTER_SECONDS", 15),
	}
}

func newImageConcurrencyController(settings imageConcurrencySettings) *imageConcurrencyController {
	return &imageConcurrencyController{
		settings: settings,
		activeFamily: map[string]int{
			"image2": 0,
			"gemini": 0,
			"grok":   0,
			"other":  0,
		},
		changed: make(chan struct{}),
	}
}

var defaultImageConcurrencyController = newImageConcurrencyController(loadImageConcurrencySettings())

func imageConcurrencyFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "gpt-image-2"):
		return "image2"
	case strings.Contains(model, "gemini") && strings.Contains(model, "image"):
		return "gemini"
	case strings.HasPrefix(model, "grok-imagine-image"):
		return "grok"
	default:
		return "other"
	}
}

func (controller *imageConcurrencyController) canStartLocked(waiter *imageConcurrencyWaiter) bool {
	return controller.activeTotal < controller.settings.globalLimit &&
		controller.activeFamily[waiter.family] < controller.settings.familyLimits[waiter.family] &&
		(!waiter.isEdit || controller.activeEdits < controller.settings.editLimit)
}

func (controller *imageConcurrencyController) startLocked(waiter *imageConcurrencyWaiter) {
	controller.activeTotal++
	controller.activeFamily[waiter.family]++
	if waiter.isEdit {
		controller.activeEdits++
	}
}

func (controller *imageConcurrencyController) signalLocked() {
	close(controller.changed)
	controller.changed = make(chan struct{})
}

func (controller *imageConcurrencyController) removeWaiterLocked(target *imageConcurrencyWaiter) {
	for index, waiter := range controller.waiters {
		if waiter != target {
			continue
		}
		controller.waiters = append(controller.waiters[:index], controller.waiters[index+1:]...)
		controller.signalLocked()
		return
	}
}

func (controller *imageConcurrencyController) acquire(ctx context.Context, model string, isEdit bool) (func(), error) {
	waiter := &imageConcurrencyWaiter{family: imageConcurrencyFamily(model), isEdit: isEdit}
	deadline := time.Now().Add(controller.settings.queueTimeout)

	controller.mu.Lock()
	if len(controller.waiters) == 0 && controller.canStartLocked(waiter) {
		controller.startLocked(waiter)
		controller.mu.Unlock()
		return controller.releaseFunc(waiter), nil
	}
	if controller.settings.queueCapacity == 0 || len(controller.waiters) >= controller.settings.queueCapacity {
		controller.mu.Unlock()
		return nil, ErrImageQueueFull
	}
	controller.waiters = append(controller.waiters, waiter)

	for {
		if controller.waiters[0] == waiter && controller.canStartLocked(waiter) {
			controller.waiters = controller.waiters[1:]
			controller.startLocked(waiter)
			controller.signalLocked()
			controller.mu.Unlock()
			return controller.releaseFunc(waiter), nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			controller.removeWaiterLocked(waiter)
			controller.mu.Unlock()
			return nil, ErrImageQueueTimedOut
		}
		changed := controller.changed
		controller.mu.Unlock()

		timer := time.NewTimer(remaining)
		select {
		case <-changed:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			controller.mu.Lock()
			controller.removeWaiterLocked(waiter)
			controller.mu.Unlock()
			return nil, ctx.Err()
		}

		controller.mu.Lock()
	}
}

func (controller *imageConcurrencyController) releaseFunc(waiter *imageConcurrencyWaiter) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			controller.mu.Lock()
			controller.activeTotal--
			controller.activeFamily[waiter.family]--
			if waiter.isEdit {
				controller.activeEdits--
			}
			controller.signalLocked()
			controller.mu.Unlock()
		})
	}
}

func AcquireImageConcurrency(ctx context.Context, model string, isEdit bool) (func(), error) {
	return defaultImageConcurrencyController.acquire(ctx, model, isEdit)
}

func ImageQueueRetryAfterSeconds() int {
	if defaultImageConcurrencyController.settings.retryAfter < 1 {
		return 1
	}
	return defaultImageConcurrencyController.settings.retryAfter
}
