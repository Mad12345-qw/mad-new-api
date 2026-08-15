package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func imageTestSettings(global, image2, other, edits, queue int, timeout time.Duration) imageConcurrencySettings {
	return imageConcurrencySettings{
		globalLimit: global,
		editLimit:   edits,
		familyLimits: map[string]int{
			"image2": image2,
			"gemini": other,
			"grok":   other,
			"other":  other,
		},
		queueCapacity: queue,
		queueTimeout:  timeout,
		retryAfter:    15,
	}
}

func imageControllerCounts(controller *imageConcurrencyController) (int, int) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.activeTotal, len(controller.waiters)
}

func waitForImageControllerCounts(t *testing.T, controller *imageConcurrencyController, active, waiting int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		actualActive, actualWaiting := imageControllerCounts(controller)
		if actualActive == active && actualWaiting == waiting {
			return
		}
		time.Sleep(time.Millisecond)
	}
	actualActive, actualWaiting := imageControllerCounts(controller)
	t.Fatalf("controller counts = active %d, waiting %d; want active %d, waiting %d", actualActive, actualWaiting, active, waiting)
}

func TestImageConcurrencyFamily(t *testing.T) {
	tests := map[string]string{
		"gpt-image-2":                       "image2",
		"gpt-image-2-4k":                    "image2",
		"gemini-3.1-flash-image-preview":    "gemini",
		"gemini-3.1-flash-image-preview-4K": "gemini",
		"grok-imagine-image":                "grok",
		"grok-imagine-image-quality":        "grok",
		"future-provider-image-model":       "other",
	}
	for model, expected := range tests {
		if actual := imageConcurrencyFamily(model); actual != expected {
			t.Fatalf("imageConcurrencyFamily(%q) = %q, want %q", model, actual, expected)
		}
	}
}

func TestImageConcurrencyQueuesThenReleases(t *testing.T) {
	controller := newImageConcurrencyController(imageTestSettings(1, 1, 1, 1, 1, time.Second))
	release, err := controller.acquire(context.Background(), "gpt-image-2", false)
	if err != nil {
		t.Fatal(err)
	}

	completed := make(chan error, 1)
	go func() {
		waiterRelease, waiterErr := controller.acquire(context.Background(), "grok-imagine-image", false)
		if waiterErr == nil {
			waiterRelease()
		}
		completed <- waiterErr
	}()
	waitForImageControllerCounts(t, controller, 1, 1)
	if _, err := controller.acquire(context.Background(), "future-image-model", false); !errors.Is(err, ErrImageQueueFull) {
		t.Fatalf("full queue error = %v, want %v", err, ErrImageQueueFull)
	}
	release()
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	waitForImageControllerCounts(t, controller, 0, 0)
}

func TestImageConcurrencyQueueTimeout(t *testing.T) {
	controller := newImageConcurrencyController(imageTestSettings(1, 1, 1, 1, 1, 20*time.Millisecond))
	release, err := controller.acquire(context.Background(), "gpt-image-2", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := controller.acquire(context.Background(), "grok-imagine-image", false); !errors.Is(err, ErrImageQueueTimedOut) {
		t.Fatalf("queue timeout error = %v, want %v", err, ErrImageQueueTimedOut)
	}
	waitForImageControllerCounts(t, controller, 1, 0)
}

func TestImageEditLimitDoesNotBlockGeneration(t *testing.T) {
	controller := newImageConcurrencyController(imageTestSettings(2, 2, 2, 1, 1, time.Second))
	releaseEdit, err := controller.acquire(context.Background(), "gpt-image-2", true)
	if err != nil {
		t.Fatal(err)
	}
	releaseGeneration, err := controller.acquire(context.Background(), "grok-imagine-image", false)
	if err != nil {
		t.Fatal(err)
	}
	waitForImageControllerCounts(t, controller, 2, 0)
	releaseGeneration()
	releaseEdit()
	waitForImageControllerCounts(t, controller, 0, 0)
}

func TestImage2OneHundredWithBoundedQueue(t *testing.T) {
	controller := newImageConcurrencyController(imageTestSettings(100, 100, 30, 30, 20, time.Second))
	releases := make([]func(), 0, 100)
	for index := 0; index < 100; index++ {
		release, err := controller.acquire(context.Background(), "gpt-image-2", false)
		if err != nil {
			t.Fatalf("active request %d: %v", index, err)
		}
		releases = append(releases, release)
	}

	var waiters sync.WaitGroup
	waiters.Add(20)
	waiterErrors := make(chan error, 20)
	for index := 0; index < 20; index++ {
		go func() {
			defer waiters.Done()
			release, err := controller.acquire(context.Background(), "gpt-image-2", false)
			if err == nil {
				release()
			}
			waiterErrors <- err
		}()
	}
	waitForImageControllerCounts(t, controller, 100, 20)
	for index := 0; index < 10; index++ {
		if _, err := controller.acquire(context.Background(), "gpt-image-2", false); !errors.Is(err, ErrImageQueueFull) {
			t.Fatalf("overflow request %d error = %v, want %v", index, err, ErrImageQueueFull)
		}
	}
	for _, release := range releases {
		release()
	}
	waiters.Wait()
	close(waiterErrors)
	for err := range waiterErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	waitForImageControllerCounts(t, controller, 0, 0)
}
