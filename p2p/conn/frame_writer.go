package conn

import "io"

// frameWriter is a bufio.Writer that can also flush only whole SecretConnection
// frames. It has no Close so protoio's Close never reaches the conn.
type frameWriter struct {
	w   io.Writer
	buf []byte
	err error
}

func newFrameWriter(w io.Writer, size int) *frameWriter {
	return &frameWriter{w: w, buf: make([]byte, 0, size)}
}

// Buffered returns the number of bytes waiting to be written.
func (b *frameWriter) Buffered() int { return len(b.buf) }

// Write buffers p, writing out a full buffer whenever it fills up, like bufio.
func (b *frameWriter) Write(p []byte) (nn int, err error) {
	for len(p) > cap(b.buf)-len(b.buf) && b.err == nil {
		var n int
		if len(b.buf) == 0 {
			// large write with an empty buffer: skip the copy.
			n, b.err = b.w.Write(p)
		} else {
			n = copy(b.buf[len(b.buf):cap(b.buf)], p)
			b.buf = b.buf[:len(b.buf)+n]
			_ = b.Flush()
		}
		nn += n
		p = p[n:]
	}
	if b.err != nil {
		return nn, b.err
	}
	b.buf = append(b.buf, p...)
	return nn + len(p), nil
}

// Flush writes all buffered bytes.
func (b *frameWriter) Flush() error {
	return b.flushPrefix(len(b.buf))
}

// FlushWhole writes the largest multiple of frame bytes and keeps the rest.
func (b *frameWriter) FlushWhole(frame int) error {
	return b.flushPrefix(len(b.buf) - len(b.buf)%frame)
}

func (b *frameWriter) flushPrefix(n int) error {
	if b.err != nil {
		return b.err
	}
	if n == 0 {
		return nil
	}
	m, err := b.w.Write(b.buf[:n])
	if m < n && err == nil {
		err = io.ErrShortWrite
	}
	// keep unwritten bytes in order, as bufio does.
	b.buf = b.buf[:copy(b.buf, b.buf[m:])]
	b.err = err
	return err
}
