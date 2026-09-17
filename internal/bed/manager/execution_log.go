package manager

import (
	"fmt"
	"os"
	"path/filepath"
)

// Background logs belong to Execution and live outside user-visible BedFS and
// Store sync paths. Executor replacement retains them; forgetting the Bed ends
// their lifetime. Finished logs are retained for 24h within a live daemon/Bed.
type executionLog struct {
	path   string
	writer *os.File
	err    error
}

func newExecutionLog(bedDir string) (*executionLog, error) {
	dir := filepath.Join(bedDir, "executions")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("execution log directory must be private")
	}
	f, err := os.CreateTemp(dir, "command-*.log")
	if err != nil {
		return nil, err
	}
	return &executionLog{path: f.Name(), writer: f}, nil
}
func (l *executionLog) discard() {
	if l.writer != nil {
		l.writer.Close()
	}
	os.Remove(l.path)
}
func (e *Execution) removeLog() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.log == nil {
		return nil
	}
	if e.log.writer != nil {
		return fmt.Errorf("execution log is still active")
	}
	err := os.Remove(e.log.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// OpenBackgroundLog returns a bounded snapshot. Cursor is a byte offset;
// callers can poll partial lines independently of the bounded fragment history.
func (e *Execution) OpenBackgroundLog(cursor int64) (file *os.File, next int64, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cursor < 0 {
		return nil, 0, fmt.Errorf("cursor must be nonnegative")
	}
	if e.log == nil {
		return nil, 0, fmt.Errorf("execution is not a background command")
	}
	if e.log.err != nil {
		return nil, 0, fmt.Errorf("capture execution output: %w", e.log.err)
	}
	f, err := os.Open(e.log.path)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	next = info.Size()
	if cursor > next {
		cursor = next
	}
	if _, err = f.Seek(cursor, 0); err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, next, nil
}
func (e *Execution) OutputError() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.log != nil {
		return e.log.err
	}
	return nil
}
func (r *ExecutionRegistry) forgetBed(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.order[:0]
	for _, id := range r.order {
		e, ok := r.executions[id]
		if !ok {
			continue
		}
		if e.BedID == name {
			delete(r.executions, id)
		} else {
			kept = append(kept, id)
		}
	}
	r.order = kept
}

// Capture precedes line framing so live logs include unterminated output while
// native output-fragment semantics remain stable.
type executionLogWriter struct{ execution *Execution }

func (w executionLogWriter) Write(p []byte) (int, error) {
	e := w.execution
	e.mu.Lock()
	defer e.mu.Unlock()
	n, err := e.log.writer.Write(p)
	if err != nil {
		e.log.err = err
		go e.RequestStop(CauseOutputFailure)
	}
	return n, err
}
