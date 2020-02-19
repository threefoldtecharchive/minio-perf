package main

import "context"

type destCtx struct {
	context.Context
	destructors []func()
}

func (c *destCtx) destroy() {
	for i := len(c.destructors) - 1; i >= 0; i-- {
		c.destructors[i]()
	}
	c.destructors = nil
}

// WithDestructor create a context with destructor
func WithDestructor(ctx context.Context) (context.Context, context.CancelFunc) {
	parent, cancel := context.WithCancel(ctx)
	newCtx := &destCtx{Context: parent}
	destroy := func() {
		cancel()
		newCtx.destroy()
	}

	return newCtx, destroy
}

// AddDestructor to context
func AddDestructor(ctx context.Context, f func()) {
	dctx, ok := ctx.(*destCtx)
	if !ok {
		panic("context with no destructor")
	}

	dctx.destructors = append(dctx.destructors, f)
}
