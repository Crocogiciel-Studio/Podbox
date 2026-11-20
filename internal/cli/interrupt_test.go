//go:build !windows

package cli

import (
	"bufio"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestHelperInterrupt is the child of TestSecondInterruptKills: it cancels on
// the first signal and then ignores its context, like exec or attach.
func TestHelperInterrupt(t *testing.T) {
	if os.Getenv("PODBOX_TEST_INTERRUPT_HELPER") == "" {
		t.Skip("helper process")
	}
	ctx, stop := interruptContext()
	defer stop()
	os.Stdout.WriteString("ready\n")
	<-ctx.Done()
	os.Stdout.WriteString("cancelled\n")
	time.Sleep(30 * time.Second)
}

func TestSecondInterruptKills(t *testing.T) {
	c := exec.Command(os.Args[0], "-test.run=^TestHelperInterrupt$")
	c.Env = append(os.Environ(), "PODBOX_TEST_INTERRUPT_HELPER=1")
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Process.Kill()
	r := bufio.NewReader(out)
	line := func() string { l, _ := r.ReadString('\n'); return l }
	if got := line(); got != "ready\n" {
		t.Fatalf("child said %q", got)
	}
	c.Process.Signal(syscall.SIGTERM)
	if got := line(); got != "cancelled\n" {
		t.Fatalf("the first signal did not cancel the context: %q", got)
	}
	time.Sleep(200 * time.Millisecond) // the handler is removed right after
	c.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the child exited normally")
		}
	case <-time.After(5 * time.Second):
		t.Error("a second signal did not kill a command that ignores its context")
	}
}
