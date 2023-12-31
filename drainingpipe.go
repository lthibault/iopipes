package iopipes

import (
	"errors"
	"io"
	"sync"
)

// ErrPipeFull is returned from DrainingPipeWriter.Write if the pipes capacity has
// been reached.
var ErrPipeFull = errors.New("pipe full") // writer should enter draining mode

// DrainingPipeReader is a reading side of an DrainingPipe, similar to io.PipeReader.
type DrainingPipeReader struct {
	*DrainingPipeWriter
}

// DrainingPipeWriter is a writing side of a DrainingPipe, similar to io.PipeWriter.
type DrainingPipeWriter struct {
	c        sync.Cond
	m        sync.Mutex
	buffer   []byte
	draining bool
	closed   bool
	sig      chan<- struct{}
	capacity int
}

// DrainingPipe is similar to InfinitePipe except that the first write to
// an overfilled buffer will block.  For the avoidance of doubt:  the write
// that initially overflows the buffer will succeed.  This means that the
// buffer can grow arbitrarily bigger than the specified capacity.
//
// Additionally, you may supply a signal channel that will be told whenever
// the draining channel has been emptied, so that more bytes can be requested
// to be written.
//
// This pipe kind is useful when implementing simple congestion control.
//
// N.B. The Writer end of this pipe will not work with io.Copy because it
// returns an error when the pipe is full (but the pipe is still valid).
func DrainingPipe(capacity int, sig chan<- struct{}) (*DrainingPipeReader, *DrainingPipeWriter) {
	w := &DrainingPipeWriter{
		sig:      sig,
		capacity: capacity,
	}
	w.c.L = &w.m
	return &DrainingPipeReader{w}, w
}

func (r *DrainingPipeReader) Read(p []byte) (int, error) {
	r.m.Lock()
	defer r.m.Unlock()

	for len(r.buffer) == 0 && !r.closed {
		r.c.Wait()
	}

	// Copy to return value
	n := copy(p, r.buffer)

	// Move the rest of the buffer
	m := copy(r.buffer, r.buffer[n:])
	r.buffer = r.buffer[:m]

	// Signal that the drain completed
	if r.draining && len(r.buffer) == 0 && r.sig != nil {
		r.draining = false
		r.sig <- struct{}{}
	}

	// Set error to EOF, if closed and we're at the end of the buffer
	var err error
	if r.closed && len(r.buffer) == 0 {
		err = io.EOF
		if r.sig != nil {
			close(r.sig)
			r.sig = nil
		}
	}

	return n, err
}

// Close the pipe reader
func (r *DrainingPipeReader) Close() error {
	r.m.Lock()
	defer r.m.Unlock()
	r.closed = true
	r.c.Broadcast()
	return nil
}

func (w *DrainingPipeWriter) Write(p []byte) (int, error) {
	w.m.Lock()
	defer w.m.Unlock()

	// If pipe is closed we'll return an error
	if w.closed {
		return 0, io.ErrClosedPipe
	}

	// If we have more data than can fit the pipe buffer we extend the pipe and
	// return ErrPipeFull
	var err error
	if len(w.buffer)+len(p) > w.capacity {
		w.draining = true
		err = ErrPipeFull
	}

	// Remember if it was empty, so we know if we should signal
	empty := len(w.buffer) == 0

	// Append data
	w.buffer = append(w.buffer, p...)

	// Signal threads waiting, if we just added data
	if empty && len(p) > 0 {
		w.c.Broadcast()
	}

	return len(p), err
}

// Close will close the stream
func (w *DrainingPipeWriter) Close() error {
	w.m.Lock()
	defer w.m.Unlock()

	w.closed = true
	w.c.Broadcast()
	return nil
}
