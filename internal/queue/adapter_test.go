package queue

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	cmfqueue "github.com/wuwuseo/cmf/queue"
)

func TestTaskLogRemainsBounded(t *testing.T) {
	buf := newBoundedBuffer(maxTaskLogBytes)
	chunk := []byte(strings.Repeat("x", 4096))
	for n := 0; n < 256; n++ {
		if written, err := buf.Write(chunk); err != nil || written != len(chunk) {
			t.Fatalf("write=%d, err=%v", written, err)
		}
	}
	if len(buf.buf) != maxTaskLogBytes || buf.discarded != 256*4096-maxTaskLogBytes {
		t.Fatalf("unbounded log or wrong discard count: len=%d discarded=%d", len(buf.buf), buf.discarded)
	}
}

func TestCanceledTaskDoesNotStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	handler := adaptTaskFunc("test", func(context.Context, io.Writer) error { called = true; return nil })
	if err := handler(ctx, &cmfqueue.Task{}); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled task started: called=%v err=%v", called, err)
	}
}
