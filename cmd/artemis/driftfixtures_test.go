package main

import (
	"context"
)

type staticLister struct{ keys []string }

func (l staticLister) ListPrefix(context.Context, string) ([]string, error) { return l.keys, nil }

func (l staticLister) PrefixBytes(context.Context, string) (int64, error) {
	return int64(len(l.keys)), nil
}
