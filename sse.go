package gmcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const sseContentType = "text/event-stream"

// sseWriter writes one JSON-RPC message per SSE event and keeps the response
// stream open until the caller finishes.
type sseWriter struct {
	mu     sync.Mutex
	w      io.Writer
	flush  func()
	closed bool
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("gmcp: response writer cannot stream")
	}
	header := w.Header()
	header.Set("Content-Type", sseContentType)
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &sseWriter{w: w, flush: flusher.Flush}, nil
}

// close makes later writes fail, which stops subscribers from touching a
// response the server already finished.
func (s *sseWriter) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *sseWriter) write(message json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("gmcp: stream is closed")
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", message); err != nil {
		return err
	}
	s.flush()
	return nil
}

// comment emits an SSE comment, which carries no payload and keeps idle
// connections from being closed by intermediaries.
func (s *sseWriter) comment(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	_, _ = fmt.Fprintf(s.w, ": %s\n\n", text)
	s.flush()
}

// sseReader yields the data payload of each SSE event, skipping comments and
// fields other than data.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &sseReader{scanner: scanner}
}

// next returns the next event payload, or io.EOF when the stream ends.
func (s *sseReader) next() (json.RawMessage, error) {
	var data []string
	for s.scanner.Scan() {
		line := s.scanner.Text()
		switch {
		case line == "":
			if len(data) == 0 {
				continue
			}
			return json.RawMessage(strings.Join(data, "\n")), nil
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := s.scanner.Err(); err != nil {
		return nil, err
	}
	if len(data) > 0 {
		return json.RawMessage(strings.Join(data, "\n")), nil
	}
	return nil, io.EOF
}
