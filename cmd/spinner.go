package cmd

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const trapicheTitle = `
░▒▓████████▓▒░▒▓███████▓▒░ ░▒▓██████▓▒░░▒▓███████▓▒░░▒▓█▓▒░░▒▓██████▓▒░░▒▓█▓▒░░▒▓█▓▒░▒▓████████▓▒░
   ░▒▓█▓▒░   ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░
   ░▒▓█▓▒░   ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░
   ░▒▓█▓▒░   ░▒▓███████▓▒░░▒▓████████▓▒░▒▓███████▓▒░░▒▓█▓▒░▒▓█▓▒░      ░▒▓████████▓▒░▒▓██████▓▒░
   ░▒▓█▓▒░   ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░      ░▒▓█▓▒░▒▓█▓▒░      ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░
   ░▒▓█▓▒░   ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░      ░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░
   ░▒▓█▓▒░   ░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░░▒▓█▓▒░▒▓█▓▒░      ░▒▓█▓▒░░▒▓██████▓▒░░▒▓█▓▒░░▒▓█▓▒░▒▓████████▓▒░

`

// isTerminal returns true if os.Stdout is a real TTY (not a pipe/test).
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Spinner shows an animated braille spinner on a TTY; is a no-op otherwise.
type Spinner struct {
	frames []string
	msg    string
	done   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	tty    bool
}

func newSpinner(msg string) *Spinner {
	return &Spinner{
		frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		msg:    msg,
		done:   make(chan struct{}),
		tty:    isTerminal(),
	}
}

func (s *Spinner) Start() {
	if !s.tty {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for i := 0; ; i++ {
			select {
			case <-s.done:
				return
			default:
				s.mu.Lock()
				fmt.Printf("\r%s %s ", s.frames[i%len(s.frames)], s.msg)
				s.mu.Unlock()
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()
}

// Stop halts the spinner and prints a success line.
func (s *Spinner) Stop(finalMsg string) {
	if s.tty {
		close(s.done)
		s.wg.Wait()
		s.mu.Lock()
		fmt.Printf("\r✓ %s\n", finalMsg)
		s.mu.Unlock()
	} else {
		fmt.Printf("✓ %s\n", finalMsg)
	}
}

// Fail halts the spinner and prints a failure line.
func (s *Spinner) Fail(finalMsg string) {
	if s.tty {
		close(s.done)
		s.wg.Wait()
		s.mu.Lock()
		fmt.Printf("\r✗ %s\n", finalMsg)
		s.mu.Unlock()
	} else {
		fmt.Printf("✗ %s\n", finalMsg)
	}
}

// ClearLine erases the current spinner line — use before printing log output.
func (s *Spinner) ClearLine() {
	if !s.tty {
		return
	}
	s.mu.Lock()
	fmt.Printf("\r%s\r", strings.Repeat(" ", len(s.msg)+6))
	s.mu.Unlock()
}
