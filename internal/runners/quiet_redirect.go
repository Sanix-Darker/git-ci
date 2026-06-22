package runners

import (
	"os"
	"sync"
)

// quietRedirectState is the package-level state for the ref-counted
// os.Stdout/os.Stderr -> /dev/null redirection used while cfg.Quiet=true.
//
// The redirect is shared across all runners within the same process so that
// parallel job execution (runJobsParallel) does not race on the global
// os.Stdout/os.Stderr variables. The first runner to acquire becomes the
// "installer" and is responsible for opening /dev/null and grabbing the
// originals; subsequent acquires only bump the ref count. The last release
// restores os.Stdout/os.Stderr and closes the dev/null handle.
//
// We use a Go-level variable assignment rather than syscall.Dup2 because the
// documented leaks (fmt.Printf / io.Copy(os.Stdout, ...) in podman.go and
// docker.go) all run inside the Go process. Subprocesses that explicitly set
// cmd.Stdout = os.Stdout also pick up the swap. Subprocesses that leave
// cmd.Stdout nil inherit the OS fd 1/2 unchanged; those are a separate
// concern and out of scope for the fix described in FEATURES.md.
type quietRedirectState struct {
	mu        sync.Mutex
	count     int
	origOut   *os.File
	origErr   *os.File
	devNull   *os.File
	installed bool
}

// quietRef is the package-wide instance. Tests in this package may call
// acquireQuietRedirect / releaseQuietRedirect directly without interfering
// with runner-level wiring.
var quietRef quietRedirectState

// acquireQuietRedirect claims a ref-counted slot for quiet-mode stdout/stderr
// redirection. Returns true if a ref count slot was successfully claimed
// (so the caller knows their deferred release will do meaningful work),
// false if Quiet was off or the dev/null open failed (in which case callers
// should not defer release).
//
// Idempotent: concurrent acquires serialize on the mutex. The first acquirer
// does the actual swap (captures originals, opens /dev/null, swaps the
// variables); subsequent acquirers also take the mutex so ref-count bumping
// itself is contention-bound — never assume the slow path is mutex-free
// just because no syscall happens inside.
//
// acquireQuietRedirect and releaseQuietRedirect MUST be called in pairs.
// releaseQuietRedirect is safe to call when acquire returned false (no-op).
func acquireQuietRedirect(quiet bool) bool {
	if !quiet {
		return false
	}
	quietRef.mu.Lock()
	defer quietRef.mu.Unlock()

	if quietRef.count == 0 {
		// First acquirer: capture originals and install /dev/null.
		d, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			// Failed to open /dev/null. The host filesystem should
			// always have it, but if not, swallow quietly rather than
			// crashing the runner.
			return false
		}
		quietRef.origOut = os.Stdout
		quietRef.origErr = os.Stderr
		quietRef.devNull = d
		os.Stdout = d
		os.Stderr = d
		quietRef.installed = true
	}
	quietRef.count++
	return true
}

// releaseQuietRedirect decrements the ref count and, if the count reaches
// zero, restores os.Stdout/os.Stderr and closes the /dev/null handle.
//
// Safe to call when acquire returned false; in that case it is a no-op.
// Safe to call concurrently with other acquires/releases (mutex-protected).
// Restore happens BEFORE Close so writes during cleanup of one runner still
// see the swap if another runner is mid-execution (ref count > 0).
func releaseQuietRedirect() {
	quietRef.mu.Lock()
	defer quietRef.mu.Unlock()

	if quietRef.count == 0 {
		// Stray release — nothing to do. Could happen if a test
		// double-released or if acquire silently failed and the test
		// still deferred release.
		return
	}

	quietRef.count--
	if quietRef.count > 0 {
		return
	}

	if !quietRef.installed {
		return
	}

	os.Stdout = quietRef.origOut
	os.Stderr = quietRef.origErr

	if quietRef.devNull != nil {
		_ = quietRef.devNull.Close()
		quietRef.devNull = nil
	}
	quietRef.origOut = nil
	quietRef.origErr = nil
	quietRef.installed = false
}

// quietRedirectActive is an internal helper that asks "is the redirect
// currently installed?". Used by tests to assert ref-count boundaries
// without leaking the package internals.
func quietRedirectActive() bool {
	quietRef.mu.Lock()
	defer quietRef.mu.Unlock()
	return quietRef.installed
}

// quietRedirectRefCount returns the current ref count. Test-only.
func quietRedirectRefCount() int {
	quietRef.mu.Lock()
	defer quietRef.mu.Unlock()
	return quietRef.count
}

// ensureRelease is a tiny helper for callers that want to bind the
// release to a fixed scope. Equivalent to defer releaseQuietRedirect but
// the helper exists so tests can verify panic-safety without writing
// the same defer pattern in each test.
//
// (No keep-alive placeholder imports are needed; the helper itself
// returns immediately and is exported via defer-style usage only.)
