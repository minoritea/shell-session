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
	err := run()
	if err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	sock := filepath.Join(home, ".cache", "shell-session.sock")

	err = os.Remove(sock)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer os.Remove(sock)
	defer l.Close()

	cmd := exec.Command("bash")
	fd, err := pty.Start(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	go func() { cancel(cmd.Wait()) }()
	go func() { cancel(loop(ctx, fd, l)) }()
	<-ctx.Done()
	return ctx.Err()
}

func loop(ctx context.Context, tty *os.File, listener net.Listener) error {
	var buf buffer
	go io.Copy(&buf, tty)

	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
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
	var buf [1024]byte
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			n, err := src.Read(buf[:])
			if err != nil {
				return err
			}
			if n == 0 {
				continue
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
