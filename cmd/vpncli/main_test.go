package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The child half of TestInterrupted. It reports when the context is canceled
// and then refuses to finish, which is what a stuck command looks like.
func interruptChild() {
	ctx, stop := interrupted(context.Background())
	defer stop()

	os.Stdout.WriteString("ready\n")
	<-ctx.Done()
	os.Stdout.WriteString("canceled\n")

	select {}
}

// The first Ctrl-C has to cancel, and the second has to kill: a question
// waiting on a terminal is a blocked read, and a user pressing Ctrl-C twice
// has said clearly enough that they want out.
func TestInterrupted(t *testing.T) {
	if os.Getenv("VPNCLI_INTERRUPT_CHILD") == "1" {
		interruptChild()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestInterrupted")
	cmd.Env = append(os.Environ(), "VPNCLI_INTERRUPT_CHILD=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	defer cmd.Process.Kill()

	lines := bufio.NewScanner(stdout)
	read := func(want string) {
		t.Helper()
		done := make(chan string, 1)
		go func() {
			for lines.Scan() {
				if line := strings.TrimSpace(lines.Text()); line == want {
					done <- line
					return
				}
			}
			done <- ""
		}()
		select {
		case got := <-done:
			if got != want {
				t.Fatalf("child never said %q", want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}

	read("ready")

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("first interrupt: %v", err)
	}
	read("canceled")

	// The child is now stuck on purpose. Only the default disposition can end
	// it, which is what the first signal was supposed to leave behind.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("second interrupt: %v", err)
	}

	// Bounded, because the failure this guards against is a child that never
	// dies: waiting for it would burn the whole test timeout and report a
	// stack trace instead of the one useful sentence.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err = <-waited:
	case <-time.After(20 * time.Second):
		t.Fatal("the child survived a second interrupt: the handler is still installed")
	}

	var status syscall.WaitStatus
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status = exit.Sys().(syscall.WaitStatus)
	}
	if !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Fatalf("child exited with %v, want killed by SIGINT", err)
	}
}

// The window this closes is small and real: a second interrupt arriving
// between the cancel and the handler being removed used to be swallowed, and
// the command could then not be interrupted at all.
func TestInterruptedRestoresTheDefaultBeforeCanceling(t *testing.T) {
	if os.Getenv("VPNCLI_INTERRUPT_CHILD") == "1" {
		interruptChild()
		return
	}

	// Racing the two is the point, so it is run enough times to catch an
	// ordering that only usually holds.
	for range 5 {
		cmd := exec.Command(os.Args[0], "-test.run=TestInterruptedRestoresTheDefaultBeforeCanceling")
		cmd.Env = append(os.Environ(), "VPNCLI_INTERRUPT_CHILD=1")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("stdout pipe: %v", err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting the child: %v", err)
		}

		lines := bufio.NewScanner(stdout)
		for lines.Scan() && strings.TrimSpace(lines.Text()) != "canceled" {
			if strings.TrimSpace(lines.Text()) == "ready" {
				cmd.Process.Signal(os.Interrupt)
			}
		}

		// The moment the cancel is visible, the default has to be back.
		cmd.Process.Signal(os.Interrupt)

		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		select {
		case <-waited:
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
			t.Fatal("a second interrupt sent the instant the cancel was seen did not kill the child")
		}
	}
}
