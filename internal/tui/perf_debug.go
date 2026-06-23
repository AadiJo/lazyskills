package tui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var perfDebug struct {
	once sync.Once
	mu   sync.Mutex
	file *os.File
	path string
}

func perfLogf(format string, args ...any) {
	perfDebug.once.Do(func() {
		perfDebug.path = os.Getenv("LAZYSKILLS_PERF_LOG")
		if perfDebug.path == "" {
			return
		}
		file, err := os.OpenFile(perfDebug.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			perfDebug.file = file
		}
	})
	if perfDebug.file == nil {
		return
	}
	perfDebug.mu.Lock()
	defer perfDebug.mu.Unlock()
	_, _ = fmt.Fprintf(perfDebug.file, "%s ", time.Now().Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(perfDebug.file, format, args...)
	_, _ = fmt.Fprintln(perfDebug.file)
}
