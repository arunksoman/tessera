package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
)

// MaxMessage caps one protocol message. A Batch of thousands of rows fits easily, and a
// peer cannot make the other side buffer without limit.
const MaxMessage = 64 << 20

// message is one line of the protocol. A request has Method, a response has Result or
// Error, and a cancel names a request the sender made earlier.
type message struct {
	ID     uint64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wireError      `json:"error,omitempty"`
	Cancel uint64          `json:"cancel,omitempty"`
}

type wireError struct {
	Code    string `json:"code,omitempty"` // eof, canceled, deadline, or empty
	Message string `json:"message"`
}

// RemoteError is an error reported by the other side of the protocol.
type RemoteError struct {
	Code, Message string
}

func (e *RemoteError) Error() string { return e.Message }

// Unwrap lets errors.Is match context errors across the process boundary.
func (e *RemoteError) Unwrap() error {
	switch e.Code {
	case "canceled":
		return context.Canceled
	case "deadline":
		return context.DeadlineExceeded
	}
	return nil
}

func encodeError(err error) *wireError {
	switch {
	case errors.Is(err, io.EOF):
		return &wireError{Code: "eof", Message: "EOF"}
	case errors.Is(err, context.Canceled):
		return &wireError{Code: "canceled", Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &wireError{Code: "deadline", Message: err.Error()}
	}
	return &wireError{Message: err.Error()}
}

func decodeError(w *wireError) error {
	if w.Code == "eof" {
		return io.EOF
	}
	return &RemoteError{Code: w.Code, Message: cleanText(w.Message, 4096)}
}

// cleanText drops control characters and caps length, because the other side's text can
// reach a terminal.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// handler serves one incoming request.
type handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// ErrClosed reports that the other process went away.
var ErrClosed = errors.New("plugin connection closed")

// cancelGrace is how long a cancelled call waits for the other side to finish, so a
// cancelled statement is stopped before the caller moves on.
const cancelGrace = 5 * time.Second

// peer is one end of a bidirectional, multiplexed connection. Either side may call the other.
type peer struct {
	r       *bufio.Reader
	w       *bufio.Writer
	wmu     sync.Mutex
	handle  handler
	mu      sync.Mutex
	next    uint64
	pending map[uint64]chan message
	serving map[uint64]context.CancelFunc
	done    chan struct{}
	err     error
	ctx     context.Context
	stop    context.CancelFunc
}

func newPeer(r io.Reader, w io.Writer, h handler) *peer {
	ctx, stop := context.WithCancel(context.Background())
	return &peer{
		r: bufio.NewReaderSize(r, 64<<10), w: bufio.NewWriterSize(w, 64<<10), handle: h,
		pending: map[uint64]chan message{}, serving: map[uint64]context.CancelFunc{},
		done: make(chan struct{}), ctx: ctx, stop: stop,
	}
}

// run reads until the connection ends, then fails every outstanding call.
func (p *peer) run() error {
	err := p.read()
	p.mu.Lock()
	if errors.Is(err, io.EOF) {
		err = ErrClosed
	}
	p.err = err
	pending := p.pending
	p.pending = map[uint64]chan message{}
	p.mu.Unlock()
	p.stop()
	for _, ch := range pending {
		close(ch)
	}
	close(p.done)
	return err
}

func (p *peer) read() error {
	for {
		line, err := readLine(p.r, MaxMessage)
		if err != nil {
			return err
		}
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			return fmt.Errorf("plugin protocol: %w", err)
		}
		switch {
		case m.Method != "":
			p.serve(m)
		case m.Cancel != 0:
			p.mu.Lock()
			cancel := p.serving[m.Cancel]
			p.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		case m.ID != 0:
			p.mu.Lock()
			ch := p.pending[m.ID]
			delete(p.pending, m.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		default:
			return errors.New("plugin protocol: message with no id, method, or cancel")
		}
	}
}

// readLine returns one newline-terminated line of at most max bytes.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > max {
			return nil, fmt.Errorf("plugin protocol: message over %d bytes", max)
		}
		if err == nil {
			if buf == nil {
				return chunk[:len(chunk)-1], nil
			}
			buf = append(buf, chunk...)
			return buf[:len(buf)-1], nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			if err == io.EOF && len(buf)+len(chunk) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		buf = append(buf, chunk...)
	}
}

func (p *peer) serve(m message) {
	ctx, cancel := context.WithCancel(p.ctx)
	p.mu.Lock()
	p.serving[m.ID] = cancel
	p.mu.Unlock()
	go func() {
		defer func() {
			p.mu.Lock()
			delete(p.serving, m.ID)
			p.mu.Unlock()
			cancel()
		}()
		reply := message{ID: m.ID}
		res, err := p.safeHandle(ctx, m)
		if err == nil {
			reply.Result, err = json.Marshal(res)
		}
		if err != nil {
			reply.Result = nil
			reply.Error = encodeError(err)
		}
		p.send(reply) // a failed send means the connection is gone, which run reports
	}()
}

// safeHandle turns a panic in one request into an error, so one bad call does not end
// every connection the process serves.
func (p *peer) safeHandle(ctx context.Context, m message) (res any, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, fmt.Errorf("plugin: %s panicked: %v", m.Method, r)
		}
	}()
	return p.handle(ctx, m.Method, m.Params)
}

func (p *peer) send(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if _, err := p.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return p.w.Flush()
}

// call sends a request and decodes the result into out, which may be nil. When ctx ends, it
// asks the other side to cancel and waits a short grace period for it to stop.
func (p *peer) call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan message, 1)
	p.mu.Lock()
	if p.err != nil {
		p.mu.Unlock()
		return p.err
	}
	p.next++
	id := p.next
	p.pending[id] = ch
	p.mu.Unlock()
	if err := p.send(message{ID: id, Method: method, Params: raw}); err != nil {
		p.forget(id)
		return fmt.Errorf("%w: %v", ErrClosed, err)
	}
	var m message
	var ok bool
	select {
	case m, ok = <-ch:
	case <-ctx.Done():
		p.send(message{Cancel: id})
		t := time.NewTimer(cancelGrace)
		defer t.Stop()
		select {
		case m, ok = <-ch:
		case <-t.C:
			p.forget(id)
			return ctx.Err()
		}
	}
	if !ok {
		return p.closedErr()
	}
	if m.Error != nil {
		err := decodeError(m.Error)
		// The other side only knows it was cancelled. The caller's own reason, such as a
		// deadline, is the one to report, as an in-process tile would.
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return fmt.Errorf("%s: %w", err.Error(), context.Cause(ctx))
		}
		return err
	}
	if out == nil || len(m.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Result, out); err != nil {
		return fmt.Errorf("plugin protocol: %s result: %w", method, err)
	}
	return nil
}

func (p *peer) forget(id uint64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

func (p *peer) closedErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	return ErrClosed
}
