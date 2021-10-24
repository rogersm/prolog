package main

import (
	"flag"
	"fmt"

	"github.com/ichiban/prolog"
	"github.com/ichiban/prolog/nondet"
	"github.com/ichiban/prolog/term"
)

func main() {
	var n int
	flag.IntVar(&n, "n", 3, "the number of disks")
	flag.Parse()

	i := prolog.New(nil, nil)
	if err := i.Exec(`
hanoi(N) :- move(N, left, right, center).

move(0, _, _, _) :- !.
move(N, X, Y, Z) :-
  M is N - 1,
  move(M, X, Z, Y),
  actuate(X, Y),
  move(M, Z, Y, X).
`); err != nil {
		panic(err)
	}

	i.Register2("actuate", func(x term.Interface, y term.Interface, k func(*term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
		fmt.Printf("move a disk from %s to %s.\n", env.Resolve(x), env.Resolve(y))
		return k(env)
	})

	sols, err := i.Query(`hanoi(?).`, n)
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := sols.Close(); err != nil {
			panic(err)
		}
	}()

	if !sols.Next() {
		panic("failed")
	}
}
