package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/creack/pty"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Panic(err)
	}

	sock := filepath.Join(home, ".cache", "shell-session.sock")

	err = os.Remove(sock)
	if err != nil && !os.IsNotExist(err) {
		log.Panic(err)
	}

	l, err := net.Listen("unix", sock)
	if err != nil {
		log.Panic(err)
	}
	defer os.Remove(sock)

	cmd := exec.Command("bash")
	fd, err := pty.Start(cmd)
	if err != nil {
		log.Panic(err)
	}

	loop(fd, l)
}

func loop(tty *os.File, listener net.Listener) {
	var (
		conn   net.Conn
		ctx    context.Context
		cancel context.CancelCauseFunc
		buf    buffer
	)
	go io.Copy(&buf, tty)

	for {
		newconn, err := listener.Accept()
		if err != nil {
			log.Panic(err)
		}

		if cancel != nil {
			cancel(context.Canceled)
		}

		if ctx != nil {
			<-ctx.Done()
			err := ctx.Err()
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
				log.Panic(err)
			}
		}
		conn = newconn
		ctx, cancel = context.WithCancelCause(context.Background())
		go startSession(ctx, &buf, tty, conn)
	}
}

func startSession(ctx context.Context, buf *buffer, tty *os.File, conn net.Conn) error {
	ctx, cancel := context.WithCancelCause(ctx)
	go func() { cancel(copyCtx(ctx, tty, conn)) }()
	go func() { cancel(copyCtx(ctx, conn, buf.reader())) }()
	<-ctx.Done()
	return errors.Join(ctx.Err(), conn.Close())
}

func copyCtx(ctx context.Context, dst io.Writer, src io.Reader) error {
	for {
		var buf [1024]byte
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			n, err := src.Read(buf[:])
			if err != nil {
				return err
			}
			_, err = dst.Write(buf[:n])
			if err != nil {
				return err
			}
		}
	}
}

type buffer struct {
	sync.RWMutex
	buf []byte
}

func (b *buffer) Write(p []byte) (n int, err error) {
	b.Lock()
	defer b.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *buffer) reader() io.ReadCloser {
	return &reader{buf: b, index: 0}
}

type reader struct {
	buf    *buffer
	index  int
	closed bool
}

func (r *reader) Read(p []byte) (n int, err error) {
	r.buf.RLock()
	defer r.buf.RUnlock()
	if r.index >= len(r.buf.buf) {
		if r.closed {
			return 0, io.EOF
		}
		return 0, nil
	}
	n = copy(p, r.buf.buf[r.index:])
	r.index += n
	return n, nil
}

func (r *reader) Close() error {
	r.closed = true
	return nil
}
