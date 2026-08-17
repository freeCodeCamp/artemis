package main

import (
	"context"
)

type staticLister struct{ keys []string }

func (l staticLister) ListPrefix(context.Context, string) ([]string, error) { return l.keys, nil }
