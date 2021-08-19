package engine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ichiban/prolog/nondet"
	"github.com/ichiban/prolog/term"

	"github.com/ichiban/prolog/syntax"
)

// Conjunction executes the given goals p then q.
// Note that ,/2 in clause body (H :- B1, B2, ..., Bn.) is compiled and doesn't involve this method.
func (vm *VM) Conjunction(p, q term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return nondet.Delay(func() *nondet.Promise {
		env := *env
		pi, args, err := piArgs(p, env)
		if err != nil {
			return nondet.Error(err)
		}

		return vm.arrive(pi, args, func(env term.Env) *nondet.Promise {
			pi, args, err := piArgs(q, env)
			if err != nil {
				return nondet.Error(err)
			}

			return nondet.Delay(func() *nondet.Promise {
				env := env
				return vm.arrive(pi, args, k, &env)
			})
		}, &env)
	})
}

func (vm *VM) Disjunction(p, q term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if c, ok := env.Resolve(p).(*term.Compound); ok && c.Functor == "->" && len(c.Args) == 2 {
		return vm.IfThenElse(c.Args[0], c.Args[1], q, k, env)
	}

	return nondet.Delay(
		func() *nondet.Promise {
			env := *env
			pi, args, err := piArgs(p, env)
			if err != nil {
				return nondet.Error(err)
			}
			return vm.arrive(pi, args, k, &env)
		},
		func() *nondet.Promise {
			env := *env
			pi, args, err := piArgs(q, env)
			if err != nil {
				return nondet.Error(err)
			}
			return vm.arrive(pi, args, k, &env)
		},
	)
}

func (vm *VM) Negation(goal term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	e := *env
	ok, err := vm.Call(goal, Success, &e).Force()
	if err != nil {
		return nondet.Error(err)
	}
	if ok {
		return nondet.Bool(false)
	}
	return k(e)
}

func (vm *VM) IfThenElse(if_, then_, else_ term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	ok, err := vm.Call(if_, Success, env).Force()
	if err != nil {
		return nondet.Error(err)
	}
	if ok {
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return vm.Call(then_, k, &env)
		})
	} else {
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return vm.Call(else_, k, &env)
		})
	}
}

// Call executes goal. it succeeds if goal followed by k succeeds. A cut inside goal doesn't affect outside of Call.
func (vm *VM) Call(goal term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	var c clause
	if err := c.compileClause(term.Atom(""), goal, *env); err != nil {
		return nondet.Error(err)
	}

	return vm.exec(c.bytecode, c.xrTable, c.vars, cont{
		exit: k,
		fail: Failure,
	}, term.List(), term.List(), env, nil)
}

// Unify unifies t1 and t2 without occurs check (i.e., X = f(X) is allowed).
func Unify(t1, t2 term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if !t1.Unify(t2, false, env) {
		return nondet.Bool(false)
	}
	return k(*env)
}

// UnifyWithOccursCheck unifies t1 and t2 with occurs check (i.e., X = f(X) is not allowed).
func UnifyWithOccursCheck(t1, t2 term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if !t1.Unify(t2, true, env) {
		return nondet.Bool(false)
	}
	return k(*env)
}

// TypeVar checks if t is a variable.
func TypeVar(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(t).(term.Variable); !ok {
		return nondet.Bool(false)
	}
	return k(*env)
}

// TypeFloat checks if t is a floating-point number.
func TypeFloat(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(t).(term.Float); !ok {
		return nondet.Bool(false)
	}
	return k(*env)
}

// TypeInteger checks if t is an integer.
func TypeInteger(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(t).(term.Integer); !ok {
		return nondet.Bool(false)
	}
	return k(*env)
}

// TypeAtom checks if t is an atom.
func TypeAtom(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(t).(term.Atom); !ok {
		return nondet.Bool(false)
	}
	return k(*env)
}

// TypeCompound checks if t is a compound term.
func TypeCompound(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(t).(*term.Compound); !ok {
		return nondet.Bool(false)
	}
	return k(*env)
}

// Functor extracts the name and arity of term, or unifies term with an atomic/compound term of name and arity with
// fresh variables as arguments.
func Functor(t, name, arity term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch t := env.Resolve(t).(type) {
	case term.Variable:
		a, ok := env.Resolve(arity).(term.Integer)
		if !ok {
			return nondet.Error(typeErrorInteger(arity))
		}
		switch {
		case a < 0:
			return nondet.Error(domainErrorNotLessThanZero(a))
		case a == 0:
			return Unify(t, name, k, env)
		}

		n, ok := env.Resolve(name).(term.Atom)
		if !ok {
			return nondet.Error(typeErrorAtom(name))
		}

		vs := make([]term.Interface, a)
		for i := range vs {
			vs[i] = term.NewVariable()
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(t, &term.Compound{
				Functor: n,
				Args:    vs,
			}, k, &env)
		})
	case *term.Compound:
		pattern := term.Compound{Args: []term.Interface{name, arity}}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(&pattern, &term.Compound{Args: []term.Interface{t.Functor, term.Integer(len(t.Args))}}, k, &env)
		})
	default: // atomic
		pattern := term.Compound{Args: []term.Interface{name, arity}}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(&pattern, &term.Compound{Args: []term.Interface{t, term.Integer(0)}}, k, &env)
		})
	}
}

// Arg extracts nth argument of term as arg, or finds the argument position of arg in term as nth.
func Arg(nth, t, arg term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	c, ok := env.Resolve(t).(*term.Compound)
	if !ok {
		return nondet.Error(typeErrorCompound(t))
	}

	switch n := env.Resolve(nth).(type) {
	case term.Variable:
		pattern := term.Compound{Args: []term.Interface{n, arg}}
		ks := make([]func() *nondet.Promise, len(c.Args))
		for i := range c.Args {
			n := term.Integer(i + 1)
			arg := c.Args[i]
			ks[i] = func() *nondet.Promise {
				env := *env
				return Unify(&pattern, &term.Compound{Args: []term.Interface{n, arg}}, k, &env)
			}
		}
		return nondet.Delay(ks...)
	case term.Integer:
		if n == 0 || int(n) >= len(c.Args) {
			return nondet.Bool(false)
		}
		if n < 0 {
			return nondet.Error(domainErrorNotLessThanZero(n))
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(arg, c.Args[int(n)-1], k, &env)
		})
	default:
		return nondet.Error(typeErrorInteger(n))
	}
}

// Univ constructs list as a list which first element is the functor of term and the rest is the arguments of term, or construct a compound from list as term.
func Univ(t, list term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch t := env.Resolve(t).(type) {
	case term.Variable:
		list = env.Resolve(list)
		if list == term.Atom("[]") {
			return nondet.Error(domainErrorNotEmptyList(list))
		}
		cons, ok := list.(*term.Compound)
		if !ok || cons.Functor != "." || len(cons.Args) != 2 {
			return nondet.Error(typeErrorList(list))
		}

		f, ok := cons.Args[0].(term.Atom)
		if !ok {
			return nondet.Error(typeErrorAtom(cons.Args[0]))
		}

		var args []term.Interface
		if err := Each(cons.Args[1], func(elem term.Interface) error {
			args = append(args, elem)
			return nil
		}, *env); err != nil {
			return nondet.Error(err)
		}

		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(t, &term.Compound{
				Functor: f,
				Args:    args,
			}, k, &env)
		})
	case *term.Compound:
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(list, term.List(append([]term.Interface{t.Functor}, t.Args...)...), k, &env)
		})
	default:
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(list, term.List(t), k, &env)
		})
	}
}

// CopyTerm clones in as out.
func CopyTerm(in, out term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return Unify(copyTerm(in, nil, *env), out, k, env)
}

func copyTerm(t term.Interface, vars map[term.Variable]term.Variable, env term.Env) term.Interface {
	if vars == nil {
		vars = map[term.Variable]term.Variable{}
	}
	switch t := env.Resolve(t).(type) {
	case term.Variable:
		v, ok := vars[t]
		if !ok {
			v = term.NewVariable()
			vars[t] = v
		}
		return v
	case *term.Compound:
		c := term.Compound{
			Functor: t.Functor,
			Args:    make([]term.Interface, len(t.Args)),
		}
		for i, a := range t.Args {
			c.Args[i] = copyTerm(a, vars, env)
		}
		return &c
	default:
		return t
	}
}

// Op defines operator with priority and specifier, or removes when priority is 0.
func (vm *VM) Op(priority, specifier, operator term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	p, ok := env.Resolve(priority).(term.Integer)
	if !ok {
		return nondet.Error(typeErrorInteger(priority))
	}
	if p < 0 || p > 1200 {
		return nondet.Error(domainErrorOperatorPriority(priority))
	}

	s, ok := env.Resolve(specifier).(term.Atom)
	if !ok {
		return nondet.Error(typeErrorAtom(specifier))
	}
	switch s {
	case "xf", "yf", "xfx", "xfy", "yfx", "fx", "fy":
		break
	default:
		return nondet.Error(domainErrorOperatorSpecifier(s))
	}

	o, ok := env.Resolve(operator).(term.Atom)
	if !ok {
		return nondet.Error(typeErrorAtom(operator))
	}

	// already defined?
	for i, op := range vm.Operators {
		if op.Specifier != s || op.Name != o {
			continue
		}

		// remove it first so that we can insert it again in the right position
		copy(vm.Operators[i:], vm.Operators[i+1:])
		vm.Operators[len(vm.Operators)-1] = term.Operator{}
		vm.Operators = vm.Operators[:len(vm.Operators)-1]

		// or keep it removed.
		if p == 0 {
			return k(*env)
		}
	}

	// insert
	i := sort.Search(len(vm.Operators), func(i int) bool {
		return vm.Operators[i].Priority >= p
	})
	vm.Operators = append(vm.Operators, term.Operator{})
	copy(vm.Operators[i+1:], vm.Operators[i:])
	vm.Operators[i] = term.Operator{
		Priority:  p,
		Specifier: s,
		Name:      o,
	}

	return k(*env)
}

// CurrentOp succeeds if operator is defined with priority and specifier.
func (vm *VM) CurrentOp(priority, specifier, operator term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch p := env.Resolve(priority).(type) {
	case term.Variable:
		break
	case term.Integer:
		if p < 0 || p > 1200 {
			return nondet.Error(domainErrorOperatorPriority(priority))
		}
	default:
		return nondet.Error(domainErrorOperatorPriority(priority))
	}

	switch s := env.Resolve(specifier).(type) {
	case term.Variable:
		break
	case term.Atom:
		switch s {
		case "xf", "yf", "xfx", "xfy", "yfx", "fx", "fy":
			break
		default:
			return nondet.Error(domainErrorOperatorSpecifier(s))
		}
	default:
		return nondet.Error(domainErrorOperatorSpecifier(s))
	}

	switch env.Resolve(operator).(type) {
	case term.Variable, term.Atom:
		break
	default:
		return nondet.Error(typeErrorAtom(operator))
	}

	pattern := term.Compound{Args: []term.Interface{priority, specifier, operator}}
	ks := make([]func() *nondet.Promise, len(vm.Operators))
	for i := range vm.Operators {
		op := vm.Operators[i]
		ks[i] = func() *nondet.Promise {
			env := *env
			return Unify(&pattern, &term.Compound{Args: []term.Interface{op.Priority, op.Specifier, op.Name}}, k, &env)
		}
	}
	return nondet.Delay(ks...)
}

// Assertz appends t to the database.
func (vm *VM) Assertz(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return vm.assert(t, k, func(cs clauses, c clause) clauses {
		return append(cs, c)
	}, env)
}

// Asserta prepends t to the database.
func (vm *VM) Asserta(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return vm.assert(t, k, func(cs clauses, c clause) clauses {
		return append(clauses{c}, cs...)
	}, env)
}

func (vm *VM) assert(t term.Interface, k func(term.Env) *nondet.Promise, merge func(clauses, clause) clauses, env *term.Env) *nondet.Promise {
	pi, args, err := piArgs(t, *env)
	if err != nil {
		return nondet.Error(err)
	}

	switch pi {
	case procedureIndicator{name: ":-", arity: 1}: // directive
		name, args, err := piArgs(args.(*term.Compound).Args[0], *env)
		if err != nil {
			return nondet.Error(err)
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return vm.arrive(name, args, k, &env)
		})
	case procedureIndicator{name: ":-", arity: 2}:
		pi, _, err = piArgs(args.(*term.Compound).Args[0], *env)
		if err != nil {
			return nondet.Error(err)
		}
	}

	if vm.procedures == nil {
		vm.procedures = map[procedureIndicator]procedure{}
	}
	p, ok := vm.procedures[pi]
	if !ok {
		p = clauses{}
	}

	cs, ok := p.(clauses)
	if !ok {
		return nondet.Error(permissionErrorModifyStaticProcedure(&term.Compound{
			Functor: "/",
			Args:    []term.Interface{pi.name, pi.arity},
		}))
	}
	c := clause{pi: pi}
	if err := c.compile(t, *env); err != nil {
		return nondet.Error(err)
	}

	vm.procedures[pi] = merge(cs, c)
	return k(*env)
}

// Repeat enforces k until it returns true.
func Repeat(k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	for {
		ok, err := k(*env).Force()
		if err != nil {
			return nondet.Error(err)
		}
		if ok {
			return nondet.Bool(true)
		}
	}
}

// BagOf collects all the solutions of goal as instances, which unify with template. instances may contain duplications.
func (vm *VM) BagOf(template, goal, instances term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return vm.collectionOf(template, goal, instances, k, term.List, env)
}

// SetOf collects all the solutions of goal as instances, which unify with template. instances don't contain duplications.
func (vm *VM) SetOf(template, goal, instances term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return vm.collectionOf(template, goal, instances, k, term.Set, env)
}

func (vm *VM) collectionOf(template, goal, instances term.Interface, k func(term.Env) *nondet.Promise, agg func(...term.Interface) term.Interface, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(goal).(term.Variable); ok {
		return nondet.Error(instantiationError(goal))
	}

	qualifier, body := term.NewVariable(), term.NewVariable()
	if goal.Unify(&term.Compound{
		Functor: "^",
		Args:    []term.Interface{qualifier, body},
	}, false, env) {
		goal = body
	}

	fvs := env.FreeVariables(goal)

	freeVariables := env.FreeVariables(template, qualifier)
	groupingVariables := make([]term.Variable, 0, len(fvs))
grouping:
	for _, v := range fvs {
		for _, w := range freeVariables {
			if v == w {
				continue grouping
			}
		}
		groupingVariables = append(groupingVariables, v)
	}

	type solution struct {
		snapshots []term.Interface // snapshot of grouping variable values
		bag       []term.Interface
	}

	var solutions []solution
	_, err := vm.Call(goal, func(env term.Env) *nondet.Promise {
		snapshots := make([]term.Interface, len(groupingVariables))
		for i, v := range groupingVariables {
			snapshots[i] = env.Resolve(v)
		}

	solutions:
		for i, s := range solutions {
			env := env
			for i := range groupingVariables {
				ok, err := Compare(term.Atom("="), s.snapshots[i], snapshots[i], Success, &env).Force()
				if err != nil {
					return nondet.Error(err)
				}
				if !ok {
					continue solutions
				}
			}
			solutions[i].bag = append(s.bag, copyTerm(template, nil, env))
			return nondet.Bool(false) // ask for more solutions
		}

		solutions = append(solutions, solution{
			snapshots: snapshots,
			bag:       []term.Interface{copyTerm(template, nil, env)},
		})
		return nondet.Bool(false) // ask for more solutions
	}, env).Force()
	if err != nil {
		return nondet.Error(err)
	}

	if len(solutions) == 0 {
		return nondet.Bool(false)
	}

	ks := make([]func() *nondet.Promise, len(solutions))
	for i := range solutions {
		s := solutions[i]
		ks[i] = func() *nondet.Promise {
			env := *env
			// revert to snapshot
			for i, v := range groupingVariables {
				env = append(env, term.Binding{
					Variable: v,
					Value:    s.snapshots[i],
				})
			}

			return Unify(instances, agg(s.bag...), k, &env)
		}
	}
	return nondet.Delay(ks...)
}

// Compare compares term1 and term2 and unifies order with <, =, or >.
func Compare(order, term1, term2 term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch o := env.Resolve(order).(type) {
	case term.Variable:
		break
	case term.Atom:
		switch o {
		case "<", "=", ">":
			break
		default:
			return nondet.Error(domainErrorOrder(order))
		}
	default:
		return nondet.Error(typeErrorAtom(order))
	}

	d := term.Compare(env.Resolve(term1), env.Resolve(term2))
	switch {
	case d < 0:
		return Unify(term.Atom("<"), order, k, env)
	case d > 0:
		return Unify(term.Atom(">"), order, k, env)
	default: // d == 0:
		return Unify(term.Atom("="), order, k, env)
	}
}

// Throw throws ball as an exception.
func Throw(ball term.Interface, _ func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(ball).(term.Variable); ok {
		return nondet.Error(instantiationError(ball))
	}
	return nondet.Error(&Exception{Term: copyTerm(env.Resolve(ball), nil, *env)})
}

// Catch calls goal. If an exception is thrown and unifies with catcher, it calls recover.
func (vm *VM) Catch(goal, catcher, recover term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	ok, err := vm.Call(goal, k, env).Force()
	if err != nil {
		if ex, ok := err.(*Exception); ok && catcher.Unify(ex.Term, false, env) {
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return vm.Call(recover, k, &env)
			})
		}
		return nondet.Error(err)
	}
	return nondet.Bool(ok)
}

// CurrentPredicate matches pi with a predicate indicator of the user-defined procedures in the database.
func (vm *VM) CurrentPredicate(pi term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch pi := env.Resolve(pi).(type) {
	case term.Variable:
		break
	case *term.Compound:
		if pi.Functor != "/" || len(pi.Args) != 2 {
			return nondet.Error(typeErrorPredicateIndicator(pi))
		}
		if _, ok := env.Resolve(pi.Args[0]).(term.Atom); !ok {
			return nondet.Error(typeErrorPredicateIndicator(pi))
		}
		if _, ok := env.Resolve(pi.Args[1]).(term.Integer); !ok {
			return nondet.Error(typeErrorPredicateIndicator(pi))
		}
	default:
		return nondet.Error(typeErrorPredicateIndicator(pi))
	}

	ks := make([]func() *nondet.Promise, 0, len(vm.procedures))
	for key := range vm.procedures {
		c := term.Compound{Functor: "/", Args: []term.Interface{key.name, key.arity}}
		ks = append(ks, func() *nondet.Promise {
			env := *env
			return Unify(pi, &c, k, &env)
		})
	}
	return nondet.Delay(ks...)
}

// Retract removes a clause which matches with t.
func (vm *VM) Retract(t term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	t = term.Rulify(t, *env)

	h := t.(*term.Compound).Args[0]
	pi, _, err := piArgs(h, *env)
	if err != nil {
		return nondet.Error(err)
	}

	p, ok := vm.procedures[pi]
	if !ok {
		return nondet.Bool(false)
	}

	cs, ok := p.(clauses)
	if !ok {
		return nondet.Error(permissionErrorModifyStaticProcedure(&term.Compound{
			Functor: "/",
			Args:    []term.Interface{pi.name, pi.arity},
		}))
	}

	updated := make(clauses, 0, len(cs))
	defer func() { vm.procedures[pi] = updated }()

	for i, c := range cs {
		env := *env

		raw := term.Rulify(c.raw, env)

		if !t.Unify(raw, false, &env) {
			updated = append(updated, c)
			continue
		}

		ok, err := k(env).Force()
		if err != nil {
			updated = append(updated, cs[i+1:]...)
			return nondet.Error(err)
		}
		if ok {
			updated = append(updated, cs[i+1:]...)
			return nondet.Bool(true)
		}
	}

	return nondet.Bool(false)
}

// Abolish removes the procedure indicated by pi from the database.
func (vm *VM) Abolish(pi term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	if _, ok := env.Resolve(pi).(term.Variable); ok {
		return nondet.Error(instantiationError(pi))
	}

	c, ok := env.Resolve(pi).(*term.Compound)
	if !ok || c.Functor != "/" || len(c.Args) != 2 {
		return nondet.Error(typeErrorPredicateIndicator(pi))
	}

	if _, ok := env.Resolve(c.Args[0]).(term.Variable); ok {
		return nondet.Error(instantiationError(c.Args[0]))
	}

	name, ok := env.Resolve(c.Args[0]).(term.Atom)
	if !ok {
		return nondet.Error(typeErrorAtom(c.Args[0]))
	}

	if _, ok := env.Resolve(c.Args[1]).(term.Variable); ok {
		return nondet.Error(instantiationError(c.Args[1]))
	}

	arity, ok := env.Resolve(c.Args[1]).(term.Integer)
	if !ok {
		return nondet.Error(typeErrorInteger(c.Args[1]))
	}
	if arity < 0 {
		return nondet.Error(domainErrorNotLessThanZero(c.Args[1]))
	}

	key := procedureIndicator{name: name, arity: arity}
	if _, ok := vm.procedures[key].(clauses); !ok {
		return nondet.Error(permissionErrorModifyStaticProcedure(&term.Compound{
			Functor: "/",
			Args:    []term.Interface{name, arity},
		}))
	}
	delete(vm.procedures, key)
	return k(*env)
}

// CurrentInput unifies stream with the current input stream.
func (vm *VM) CurrentInput(stream term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch env.Resolve(stream).(type) {
	case term.Variable, *term.Stream:
		break
	default:
		return nondet.Error(domainErrorStream(stream))
	}

	return nondet.Delay(func() *nondet.Promise {
		env := *env
		return Unify(stream, vm.input, k, &env)
	})
}

// CurrentOutput unifies stream with the current output stream.
func (vm *VM) CurrentOutput(stream term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch env.Resolve(stream).(type) {
	case term.Variable, *term.Stream:
		break
	default:
		return nondet.Error(domainErrorStream(stream))
	}

	return nondet.Delay(func() *nondet.Promise {
		env := *env
		return Unify(stream, vm.output, k, &env)
	})
}

// SetInput sets streamOrAlias as the current input stream.
func (vm *VM) SetInput(streamOrAlias term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Source == nil {
		return nondet.Error(permissionErrorInputStream(streamOrAlias))
	}

	vm.input = s
	return k(*env)
}

// SetOutput sets streamOrAlias as the current output stream.
func (vm *VM) SetOutput(streamOrAlias term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Sink == nil {
		return nondet.Error(permissionErrorOutputStream(streamOrAlias))
	}

	vm.output = s
	return k(*env)
}

// Open opens SourceSink in mode and unifies with stream.
func (vm *VM) Open(SourceSink, mode, stream, options term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	var n term.Atom
	switch s := env.Resolve(SourceSink).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(SourceSink))
	case term.Atom:
		n = s
	default:
		return nondet.Error(domainErrorSourceSink(SourceSink))
	}

	var (
		s term.Stream

		flag   int
		perm   os.FileMode
		buffer bool
	)
	switch m := env.Resolve(mode).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(mode))
	case term.Atom:
		switch m {
		case "read":
			s.Mode = term.StreamModeRead
			flag = os.O_RDONLY
			buffer = true
		case "write":
			s.Mode = term.StreamModeWrite
			flag = os.O_CREATE | os.O_WRONLY
			perm = 0644
		case "append":
			s.Mode = term.StreamModeAppend
			flag = os.O_APPEND | os.O_CREATE | os.O_WRONLY
			perm = 0644
		default:
			return nondet.Error(domainErrorIOMode(m))
		}
	default:
		return nondet.Error(typeErrorAtom(mode))
	}

	if _, ok := env.Resolve(stream).(term.Variable); !ok {
		return nondet.Error(typeErrorVariable(stream))
	}

	if err := Each(env.Resolve(options), func(option term.Interface) error {
		switch o := env.Resolve(option).(type) {
		case term.Variable:
			return instantiationError(option)
		case *term.Compound:
			if len(o.Args) != 1 {
				return domainErrorStreamOption(option)
			}
			arg := o.Args[0]
			switch o.Functor {
			case "type":
				switch t := env.Resolve(arg).(type) {
				case term.Variable:
					return instantiationError(arg)
				case term.Atom:
					switch t {
					case "text":
						s.StreamType = term.StreamTypeText
						return nil
					case "binary":
						s.StreamType = term.StreamTypeBinary
						return nil
					default:
						return domainErrorStreamOption(option)
					}
				default:
					return typeErrorAtom(arg)
				}
			case "reposition":
				switch b := env.Resolve(arg).(type) {
				case term.Variable:
					return instantiationError(arg)
				case term.Atom:
					switch b {
					case "true":
						s.Reposition = true
						return nil
					case "false":
						s.Reposition = false
						return nil
					default:
						return domainErrorStreamOption(option)
					}
				default:
					return typeErrorAtom(arg)
				}
			case "alias":
				switch a := env.Resolve(arg).(type) {
				case term.Variable:
					return instantiationError(arg)
				case term.Atom:
					if _, ok := vm.streams[a]; ok {
						return permissionError(term.Atom("open"), term.Atom("source_sink"), option, term.Atom(fmt.Sprintf("%s is already defined as an alias.", a)))
					}
					s.Alias = a
					return nil
				default:
					return domainErrorStreamOption(option)
				}
			case "eof_action":
				switch a := env.Resolve(arg).(type) {
				case term.Variable:
					return instantiationError(arg)
				case term.Atom:
					switch a {
					case "error":
						s.EofAction = term.EofActionError
						return nil
					case "eof_code":
						s.EofAction = term.EofActionEOFCode
						return nil
					case "reset":
						s.EofAction = term.EofActionReset
						return nil
					default:
						return domainErrorStreamOption(option)
					}
				default:
					return domainErrorStreamOption(option)
				}
			default:
				return domainErrorStreamOption(option)
			}
		default:
			return domainErrorStreamOption(option)
		}
	}, *env); err != nil {
		return nondet.Error(err)
	}

	f, err := os.OpenFile(string(n), flag, perm)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return nondet.Error(existenceErrorSourceSink(SourceSink))
		case os.IsPermission(err):
			return nondet.Error(permissionError(term.Atom("open"), term.Atom("source_sink"), SourceSink, term.Atom(fmt.Sprintf("%s cannot be opened.", SourceSink))))
		default:
			return nondet.Error(systemError(err))
		}
	}

	switch s.Mode {
	case term.StreamModeRead:
		s.Source = f
		if buffer {
			s.Source = bufio.NewReader(s.Source)
		}
	case term.StreamModeWrite, term.StreamModeAppend:
		s.Sink = f
		if buffer {
			s.Sink = bufio.NewWriter(s.Sink)
		}
	}
	s.Closer = f

	if vm.streams == nil {
		vm.streams = map[term.Interface]*term.Stream{}
	}
	if s.Alias == "" {
		// we can't use alias for the key but all the open streams should be in streams map anyways.
		vm.streams[&s] = &s
	} else {
		vm.streams[s.Alias] = &s
	}

	return nondet.Delay(func() *nondet.Promise {
		env := *env
		return Unify(stream, &s, k, &env)
	})
}

// Close closes a stream specified by streamOrAlias.
func (vm *VM) Close(streamOrAlias, options term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	var force bool
	if err := Each(env.Resolve(options), func(option term.Interface) error {
		if _, ok := env.Resolve(option).(term.Variable); ok {
			return instantiationError(option)
		}

		switch {
		case option.Unify(&term.Compound{Functor: "force", Args: []term.Interface{term.Atom("false")}}, false, env):
			force = false
		case option.Unify(&term.Compound{Functor: "force", Args: []term.Interface{term.Atom("true")}}, false, env):
			force = true
		default:
			return domainErrorStreamOption(option)
		}
		return nil
	}, *env); err != nil {
		return nondet.Error(err)
	}

	if err := s.Closer.Close(); err != nil && !force {
		return nondet.Error(resourceError(streamOrAlias, term.Atom(err.Error())))
	}

	if s.Alias == "" {
		delete(vm.streams, s)
	} else {
		delete(vm.streams, s.Alias)
	}

	return k(*env)
}

// FlushOutput sends any buffered output to the stream.
func (vm *VM) FlushOutput(streamOrAlias term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Sink == nil {
		return nondet.Error(permissionErrorOutputStream(streamOrAlias))
	}

	type flusher interface {
		Flush() error
	}

	if f, ok := s.Sink.(flusher); ok {
		if err := f.Flush(); err != nil {
			return nondet.Error(err)
		}
	}

	return k(*env)
}

// WriteTerm outputs term to stream with options.
func (vm *VM) WriteTerm(streamOrAlias, t, options term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Sink == nil {
		return nondet.Error(permissionErrorOutputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeBinary {
		return nondet.Error(permissionErrorOutputBinaryStream(streamOrAlias))
	}

	opts := term.WriteTermOptions{Ops: vm.Operators}
	if err := Each(env.Resolve(options), func(option term.Interface) error {
		if _, ok := env.Resolve(option).(term.Variable); ok {
			return instantiationError(option)
		}

		switch {
		case option.Unify(&term.Compound{Functor: "quoted", Args: []term.Interface{term.Atom("false")}}, false, env):
			opts.Quoted = false
		case option.Unify(&term.Compound{Functor: "quoted", Args: []term.Interface{term.Atom("true")}}, false, env):
			opts.Quoted = true
		case option.Unify(&term.Compound{Functor: "ignore_ops", Args: []term.Interface{term.Atom("false")}}, false, env):
			opts.Ops = vm.Operators
		case option.Unify(&term.Compound{Functor: "ignore_ops", Args: []term.Interface{term.Atom("true")}}, false, env):
			opts.Ops = nil
		case option.Unify(&term.Compound{Functor: "numbervars", Args: []term.Interface{term.Atom("false")}}, false, env):
			opts.NumberVars = false
		case option.Unify(&term.Compound{Functor: "numbervars", Args: []term.Interface{term.Atom("true")}}, false, env):
			opts.NumberVars = true
		default:
			return domainErrorWriteOption(option)
		}
		return nil
	}, *env); err != nil {
		return nondet.Error(err)
	}

	if err := env.Resolve(t).WriteTerm(s.Sink, opts, *env); err != nil {
		return nondet.Error(err)
	}

	return k(*env)
}

// CharCode converts a single-rune Atom char to an Integer code, or vice versa.
func CharCode(char, code term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch ch := env.Resolve(char).(type) {
	case term.Variable:
		switch cd := env.Resolve(code).(type) {
		case term.Variable:
			return nondet.Error(instantiationError(&term.Compound{
				Functor: ",",
				Args:    []term.Interface{char, code},
			}))
		case term.Integer:
			r := rune(cd)

			if !utf8.ValidRune(r) {
				return nondet.Error(representationError(term.Atom("character_code"), term.Atom(fmt.Sprintf("%d is not a valid unicode code point.", r))))
			}

			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return Unify(ch, term.Atom(r), k, &env)
			})
		default:
			return nondet.Error(typeErrorInteger(code))
		}
	case term.Atom:
		rs := []rune(ch)
		if len(rs) != 1 {
			return nondet.Error(typeErrorCharacter(char))
		}

		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(code, term.Integer(rs[0]), k, &env)
		})
	default:
		return nondet.Error(typeErrorCharacter(char))
	}
}

// PutByte outputs an integer byte to a stream represented by streamOrAlias.
func (vm *VM) PutByte(streamOrAlias, byt term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Sink == nil {
		return nondet.Error(permissionErrorOutputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeText {
		return nondet.Error(permissionErrorOutputTextStream(streamOrAlias))
	}

	switch b := env.Resolve(byt).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(byt))
	case term.Integer:
		if 0 > b || 255 < b {
			return nondet.Error(typeErrorByte(byt))
		}

		if _, err := s.Sink.Write([]byte{byte(b)}); err != nil {
			return nondet.Error(systemError(err))
		}

		return k(*env)
	default:
		return nondet.Error(typeErrorByte(byt))
	}
}

// PutCode outputs code to the stream represented by streamOrAlias.
func (vm *VM) PutCode(streamOrAlias, code term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Sink == nil {
		return nondet.Error(permissionErrorOutputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeBinary {
		return nondet.Error(permissionErrorOutputBinaryStream(streamOrAlias))
	}

	switch c := env.Resolve(code).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(code))
	case term.Integer:
		r := rune(c)

		if !utf8.ValidRune(r) {
			return nondet.Error(representationError(term.Atom("character_code"), term.Atom(fmt.Sprintf("%s is not a valid unicode code point.", c))))
		}

		if _, err := s.Sink.Write([]byte(string(r))); err != nil {
			return nondet.Error(systemError(err))
		}

		return k(*env)
	default:
		return nondet.Error(typeErrorInteger(code))
	}
}

// ReadTerm reads from the stream represented by streamOrAlias and unifies with stream.
func (vm *VM) ReadTerm(streamOrAlias, out, options term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Source == nil {
		return nondet.Error(permissionErrorInputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeBinary {
		return nondet.Error(permissionErrorInputBinaryStream(streamOrAlias))
	}

	var opts struct {
		singletons    term.Variable
		variables     term.Variable
		variableNames term.Variable
	}
	if err := Each(env.Resolve(options), func(option term.Interface) error {
		if _, ok := env.Resolve(option).(term.Variable); ok {
			return instantiationError(option)
		}

		v := term.NewVariable()
		switch {
		case option.Unify(&term.Compound{Functor: "singletons", Args: []term.Interface{v}}, false, env):
			opts.singletons = v
		case option.Unify(&term.Compound{Functor: "variables", Args: []term.Interface{v}}, false, env):
			opts.variables = v
		case option.Unify(&term.Compound{Functor: "variable_names", Args: []term.Interface{v}}, false, env):
			opts.variableNames = v
		default:
			return domainErrorReadOption(option)
		}
		return nil
	}, *env); err != nil {
		return nondet.Error(err)
	}

	br, ok := s.Source.(*bufio.Reader)
	if !ok {
		return nondet.Error(errors.New("not a buffered stream"))
	}

	p := term.NewParser(br, &vm.Operators, vm.CharConversions)
	t, err := p.Term()
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			switch s.EofAction {
			case term.EofActionError:
				return nondet.Error(permissionErrorInputPastEndOfStream(streamOrAlias))
			case term.EofActionEOFCode:
				return nondet.Delay(func() *nondet.Promise {
					env := *env
					return Unify(t, term.Atom("end_of_file"), k, &env)
				})
			case term.EofActionReset:
				return nondet.Delay(func() *nondet.Promise {
					env := *env
					return vm.ReadTerm(streamOrAlias, t, options, k, &env)
				})
			default:
				return nondet.Error(systemError(fmt.Errorf("unknown EOF action: %d", s.EofAction)))
			}
		case errors.Is(err, syntax.ErrInsufficient):
			return nondet.Error(syntaxErrorInsufficient())
		case errors.As(err, &syntax.UnexpectedRuneError{}):
			return nondet.Error(syntaxErrorUnexpectedChar(term.Atom(err.Error())))
		default:
			return nondet.Error(systemError(err))
		}
	}

	var singletons, variables, variableNames []term.Interface
	for _, vc := range p.Vars {
		if vc.Count == 1 {
			singletons = append(singletons, vc.Variable)
		}
		variables = append(variables, vc.Variable)
		variableNames = append(variableNames, &term.Compound{
			Functor: "=",
			Args:    []term.Interface{term.Atom(vc.Variable), vc.Variable},
		})
		vc.Variable = term.NewVariable()
	}

	if opts.singletons != "" && !opts.singletons.Unify(term.List(singletons...), false, env) {
		return nondet.Bool(false)
	}

	if opts.variables != "" && !opts.variables.Unify(term.List(variables...), false, env) {
		return nondet.Bool(false)
	}

	if opts.variableNames != "" && !opts.variableNames.Unify(term.List(variableNames...), false, env) {
		return nondet.Bool(false)
	}

	return nondet.Delay(func() *nondet.Promise {
		env := *env
		return Unify(out, t, k, &env)
	})
}

// GetByte reads a byte from the stream represented by streamOrAlias and unifies it with inByte.
func (vm *VM) GetByte(streamOrAlias, inByte term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Source == nil {
		return nondet.Error(permissionErrorInputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeText {
		return nondet.Error(permissionErrorInputTextStream(streamOrAlias))
	}

	switch b := env.Resolve(inByte).(type) {
	case term.Variable:
		break
	case term.Integer:
		if b < 0 || b > 255 {
			nondet.Error(typeErrorInByte(inByte))
		}
	default:
		return nondet.Error(typeErrorInByte(inByte))
	}

	b := make([]byte, 1)
	_, err = s.Source.Read(b)
	switch err {
	case nil:
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(inByte, term.Integer(b[0]), k, &env)
		})
	case io.EOF:
		switch s.EofAction {
		case term.EofActionError:
			return nondet.Error(permissionErrorInputPastEndOfStream(streamOrAlias))
		case term.EofActionEOFCode:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return Unify(inByte, term.Integer(-1), k, &env)
			})
		case term.EofActionReset:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return vm.GetByte(streamOrAlias, inByte, k, &env)
			})
		default:
			return nondet.Error(systemError(fmt.Errorf("unknown EOF action: %d", s.EofAction)))
		}
	default:
		return nondet.Error(err)
	}
}

// GetChar reads a character from the stream represented by streamOrAlias and unifies it with char.
func (vm *VM) GetChar(streamOrAlias, char term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Source == nil {
		return nondet.Error(permissionErrorInputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeBinary {
		return nondet.Error(permissionErrorInputBinaryStream(streamOrAlias))
	}

	br, ok := s.Source.(*bufio.Reader)
	if !ok {
		return nondet.Error(permissionErrorInputBufferedStream(streamOrAlias))
	}

	switch c := env.Resolve(char).(type) {
	case term.Variable:
		break
	case term.Atom:
		if len([]rune(c)) != 1 {
			return nondet.Error(typeErrorInCharacter(char))
		}
	default:
		return nondet.Error(typeErrorInCharacter(char))
	}

	r, _, err := br.ReadRune()
	switch err {
	case nil:
		if r == unicode.ReplacementChar {
			return nondet.Error(representationError(term.Atom("character"), term.Atom("invalid character.")))
		}

		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(char, term.Atom(r), k, &env)
		})
	case io.EOF:
		switch s.EofAction {
		case term.EofActionError:
			return nondet.Error(permissionErrorInputPastEndOfStream(streamOrAlias))
		case term.EofActionEOFCode:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return Unify(char, term.Atom("end_of_file"), k, &env)
			})
		case term.EofActionReset:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return vm.GetChar(streamOrAlias, char, k, &env)
			})
		default:
			return nondet.Error(systemError(fmt.Errorf("unknown EOF action: %d", s.EofAction)))
		}
	default:
		return nondet.Error(systemError(err))
	}
}

// PeekByte peeks a byte from the stream represented by streamOrAlias and unifies it with inByte.
func (vm *VM) PeekByte(streamOrAlias, inByte term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Source == nil {
		return nondet.Error(permissionErrorInputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeText {
		return nondet.Error(permissionErrorInputTextStream(streamOrAlias))
	}

	br, ok := s.Source.(*bufio.Reader)
	if !ok {
		return nondet.Error(permissionErrorInputBufferedStream(streamOrAlias))
	}

	switch b := env.Resolve(inByte).(type) {
	case term.Variable:
		break
	case term.Integer:
		if b < 0 || b > 255 {
			return nondet.Error(typeErrorInByte(inByte))
		}
	default:
		return nondet.Error(typeErrorInByte(inByte))
	}

	b, err := br.Peek(1)
	switch err {
	case nil:
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(inByte, term.Integer(b[0]), k, &env)
		})
	case io.EOF:
		switch s.EofAction {
		case term.EofActionError:
			return nondet.Error(permissionErrorInputPastEndOfStream(streamOrAlias))
		case term.EofActionEOFCode:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return Unify(inByte, term.Integer(-1), k, &env)
			})
		case term.EofActionReset:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return vm.PeekByte(streamOrAlias, inByte, k, &env)
			})
		default:
			return nondet.Error(systemError(fmt.Errorf("unknown EOF action: %d", s.EofAction)))
		}
	default:
		return nondet.Error(systemError(err))
	}
}

// PeekChar peeks a rune from the stream represented by streamOrAlias and unifies it with char.
func (vm *VM) PeekChar(streamOrAlias, char term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	if s.Source == nil {
		return nondet.Error(permissionErrorInputStream(streamOrAlias))
	}

	if s.StreamType == term.StreamTypeBinary {
		return nondet.Error(permissionErrorInputBinaryStream(streamOrAlias))
	}

	br, ok := s.Source.(*bufio.Reader)
	if !ok {
		return nondet.Error(permissionErrorInputBufferedStream(streamOrAlias))
	}

	switch c := env.Resolve(char).(type) {
	case term.Variable:
		break
	case term.Atom:
		if len([]rune(c)) != 1 {
			return nondet.Error(typeErrorInCharacter(char))
		}
	default:
		return nondet.Error(typeErrorInCharacter(char))
	}

	r, _, err := br.ReadRune()
	switch err {
	case nil:
		if err := br.UnreadRune(); err != nil {
			return nondet.Error(systemError(err))
		}

		if r == unicode.ReplacementChar {
			return nondet.Error(representationError(term.Atom("character"), term.Atom("invalid character.")))
		}

		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(char, term.Atom(r), k, &env)
		})
	case io.EOF:
		switch s.EofAction {
		case term.EofActionError:
			return nondet.Error(permissionErrorInputPastEndOfStream(streamOrAlias))
		case term.EofActionEOFCode:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return Unify(char, term.Atom("end_of_file"), k, &env)
			})
		case term.EofActionReset:
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return vm.PeekChar(streamOrAlias, char, k, &env)
			})
		default:
			return nondet.Error(systemError(fmt.Errorf("unknown EOF action: %d", s.EofAction)))
		}
	default:
		return nondet.Error(systemError(err))
	}
}

var osExit = os.Exit

// Halt exits the process with exit code of n.
func (vm *VM) Halt(n term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch code := env.Resolve(n).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(n))
	case term.Integer:
		osExit(int(code))
		return k(*env)
	default:
		return nondet.Error(typeErrorInteger(n))
	}
}

// Clause unifies head and body with H and B respectively where H :- B is in the database.
func (vm *VM) Clause(head, body term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	pi, _, err := piArgs(head, *env)
	if err != nil {
		return nondet.Error(err)
	}

	switch env.Resolve(body).(type) {
	case term.Variable, term.Atom, *term.Compound:
		break
	default:
		return nondet.Error(typeErrorCallable(body))
	}

	cs, _ := vm.procedures[pi].(clauses)
	ks := make([]func() *nondet.Promise, len(cs))
	for i := range cs {
		r := term.Rulify(copyTerm(cs[i].raw, nil, *env), *env)
		ks[i] = func() *nondet.Promise {
			env := *env
			return Unify(&term.Compound{
				Functor: ":-",
				Args:    []term.Interface{head, body},
			}, r, k, &env)
		}
	}
	return nondet.Delay(ks...)
}

// AtomLength counts the runes in atom and unifies the result with length.
func AtomLength(atom, length term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch a := env.Resolve(atom).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(atom))
	case term.Atom:
		switch l := env.Resolve(length).(type) {
		case term.Variable:
			break
		case term.Integer:
			if l < 0 {
				return nondet.Error(domainErrorNotLessThanZero(length))
			}
		default:
			return nondet.Error(typeErrorInteger(length))
		}

		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(length, term.Integer(len([]rune(a))), k, &env)
		})
	default:
		return nondet.Error(typeErrorAtom(atom))
	}
}

// AtomConcat concatenates atom1 and atom2 and unifies it with atom3.
func AtomConcat(atom1, atom2, atom3 term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch a3 := env.Resolve(atom3).(type) {
	case term.Variable:
		switch a1 := env.Resolve(atom1).(type) {
		case term.Variable:
			return nondet.Error(instantiationError(&term.Compound{
				Functor: ",",
				Args:    []term.Interface{atom1, atom3},
			}))
		case term.Atom:
			switch a2 := env.Resolve(atom2).(type) {
			case term.Variable:
				return nondet.Error(instantiationError(&term.Compound{
					Functor: ",",
					Args:    []term.Interface{atom2, atom3},
				}))
			case term.Atom:
				return nondet.Delay(func() *nondet.Promise {
					env := *env
					return Unify(a1+a2, a3, k, &env)
				})
			default:
				return nondet.Error(typeErrorAtom(atom2))
			}
		default:
			return nondet.Error(typeErrorAtom(atom1))
		}
	case term.Atom:
		switch env.Resolve(atom1).(type) {
		case term.Variable, term.Atom:
			break
		default:
			return nondet.Error(typeErrorAtom(atom1))
		}

		switch env.Resolve(atom2).(type) {
		case term.Variable, term.Atom:
			break
		default:
			return nondet.Error(typeErrorAtom(atom2))
		}

		pattern := term.Compound{Args: []term.Interface{atom1, atom2}}
		ks := make([]func() *nondet.Promise, 0, len(a3)+1)
		for i := range a3 {
			a1, a2 := a3[:i], a3[i:]
			ks = append(ks, func() *nondet.Promise {
				env := *env
				return Unify(&pattern, &term.Compound{Args: []term.Interface{a1, a2}}, k, &env)
			})
		}
		ks = append(ks, func() *nondet.Promise {
			env := *env
			return Unify(&pattern, &term.Compound{Args: []term.Interface{a3, term.Atom("")}}, k, &env)
		})
		return nondet.Delay(ks...)
	default:
		return nondet.Error(typeErrorAtom(atom3))
	}
}

// SubAtom unifies subAtom with a sub atom of atom of length which appears with before runes preceding it and after runes following it.
func SubAtom(atom, before, length, after, subAtom term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch whole := env.Resolve(atom).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(atom))
	case term.Atom:
		rs := []rune(whole)

		switch b := env.Resolve(before).(type) {
		case term.Variable:
			break
		case term.Integer:
			if b < 0 {
				return nondet.Error(domainErrorNotLessThanZero(before))
			}
		default:
			return nondet.Error(typeErrorInteger(before))
		}

		switch l := env.Resolve(length).(type) {
		case term.Variable:
			break
		case term.Integer:
			if l < 0 {
				return nondet.Error(domainErrorNotLessThanZero(length))
			}
		default:
			return nondet.Error(typeErrorInteger(length))
		}

		switch a := env.Resolve(after).(type) {
		case term.Variable:
			break
		case term.Integer:
			if a < 0 {
				return nondet.Error(domainErrorNotLessThanZero(after))
			}
		default:
			return nondet.Error(typeErrorInteger(after))
		}

		switch env.Resolve(subAtom).(type) {
		case term.Variable, term.Atom:
			break
		default:
			return nondet.Error(typeErrorAtom(subAtom))
		}

		pattern := term.Compound{Args: []term.Interface{before, length, after, subAtom}}
		var ks []func() *nondet.Promise
		for i := 0; i <= len(rs); i++ {
			for j := i; j <= len(rs); j++ {
				before, length, after, subAtom := term.Integer(i), term.Integer(j-i), term.Integer(len(rs)-j), term.Atom(rs[i:j])
				ks = append(ks, func() *nondet.Promise {
					env := *env
					return Unify(&pattern, &term.Compound{Args: []term.Interface{before, length, after, subAtom}}, k, &env)
				})
			}
		}
		return nondet.Delay(ks...)
	default:
		return nondet.Error(typeErrorAtom(atom))
	}
}

// AtomChars breaks down atom into list of characters and unifies with chars, or constructs an atom from a list of
// characters chars and unifies it with atom.
func AtomChars(atom, chars term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch a := env.Resolve(atom).(type) {
	case term.Variable:
		var sb strings.Builder
		if err := Each(env.Resolve(chars), func(elem term.Interface) error {
			switch e := env.Resolve(elem).(type) {
			case term.Variable:
				return instantiationError(elem)
			case term.Atom:
				if len([]rune(e)) != 1 {
					return typeErrorCharacter(elem)
				}
				if _, err := sb.WriteString(string(e)); err != nil {
					return systemError(err)
				}
				return nil
			default:
				return typeErrorCharacter(elem)
			}
		}, *env); err != nil {
			return nondet.Error(err)
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(atom, term.Atom(sb.String()), k, &env)
		})
	case term.Atom:
		rs := []rune(a)
		cs := make([]term.Interface, len(rs))
		for i, r := range rs {
			cs[i] = term.Atom(r)
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(chars, term.List(cs...), k, &env)
		})
	default:
		return nondet.Error(typeErrorAtom(atom))
	}
}

// AtomCodes breaks up atom into a list of runes and unifies it with codes, or constructs an atom from the list of runes
// and unifies it with atom.
func AtomCodes(atom, codes term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch a := env.Resolve(atom).(type) {
	case term.Variable:
		var sb strings.Builder
		if err := Each(env.Resolve(codes), func(elem term.Interface) error {
			switch e := env.Resolve(elem).(type) {
			case term.Variable:
				return instantiationError(elem)
			case term.Integer:
				if _, err := sb.WriteRune(rune(e)); err != nil {
					return systemError(err)
				}
				return nil
			default:
				return representationError(term.Atom("character_code"), term.Atom("invalid character code."))
			}
		}, *env); err != nil {
			return nondet.Error(err)
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(atom, term.Atom(sb.String()), k, &env)
		})
	case term.Atom:
		rs := []rune(a)
		cs := make([]term.Interface, len(rs))
		for i, r := range rs {
			cs[i] = term.Integer(r)
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(codes, term.List(cs...), k, &env)
		})
	default:
		return nondet.Error(typeErrorAtom(atom))
	}
}

// NumberChars breaks up an atom representation of a number num into a list of characters and unifies it with chars, or
// constructs a number from a list of characters chars and unifies it with num.
func NumberChars(num, chars term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch n := env.Resolve(num).(type) {
	case term.Variable:
		var sb strings.Builder
		if err := Each(env.Resolve(chars), func(elem term.Interface) error {
			switch e := env.Resolve(elem).(type) {
			case term.Variable:
				return instantiationError(elem)
			case term.Atom:
				if len([]rune(e)) != 1 {
					return typeErrorCharacter(elem)
				}
				if _, err := sb.WriteString(string(e)); err != nil {
					return systemError(err)
				}
				return nil
			default:
				return typeErrorCharacter(elem)
			}
		}, *env); err != nil {
			return nondet.Error(err)
		}

		p := term.NewParser(bufio.NewReader(strings.NewReader(sb.String())), nil, nil)
		t, err := p.Number()
		switch err {
		case nil:
			break
		case term.ErrNotANumber:
			return nondet.Error(syntaxErrorNotANumber())
		default:
			return nondet.Error(systemError(err))
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(num, t, k, &env)
		})
	case term.Integer, term.Float:
		var buf bytes.Buffer
		if err := n.WriteTerm(&buf, term.DefaultWriteTermOptions, *env); err != nil {
			return nondet.Error(err)
		}
		rs := []rune(buf.String())
		cs := make([]term.Interface, len(rs))
		for i, r := range rs {
			cs[i] = term.Atom(r)
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(chars, term.List(cs...), k, &env)
		})
	default:
		return nondet.Error(typeErrorNumber(num))
	}
}

// NumberCodes breaks up an atom representation of a number num into a list of runes and unifies it with codes, or
// constructs a number from a list of runes codes and unifies it with num.
func NumberCodes(num, codes term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch n := env.Resolve(num).(type) {
	case term.Variable:
		var sb strings.Builder
		if err := Each(env.Resolve(codes), func(elem term.Interface) error {
			switch e := env.Resolve(elem).(type) {
			case term.Variable:
				return instantiationError(elem)
			case term.Integer:
				if _, err := sb.WriteRune(rune(e)); err != nil {
					return systemError(err)
				}
				return nil
			default:
				return representationError(term.Atom("character_code"), term.Atom(fmt.Sprintf("%s is not a valid character code.", elem)))
			}
		}, *env); err != nil {
			return nondet.Error(err)
		}

		p := term.NewParser(bufio.NewReader(strings.NewReader(sb.String())), nil, nil)
		t, err := p.Number()
		switch err {
		case nil:
			break
		case term.ErrNotANumber:
			return nondet.Error(syntaxErrorNotANumber())
		default:
			return nondet.Error(systemError(err))
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(num, t, k, &env)
		})
	case term.Integer, term.Float:
		var buf bytes.Buffer
		if err := n.WriteTerm(&buf, term.DefaultWriteTermOptions, *env); err != nil {
			return nondet.Error(err)
		}
		rs := []rune(buf.String())
		cs := make([]term.Interface, len(rs))
		for i, r := range rs {
			cs[i] = term.Integer(r)
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(codes, term.List(cs...), k, &env)
		})
	default:
		return nondet.Error(typeErrorNumber(num))
	}
}

// FunctionSet is a set of unary/binary functions.
type FunctionSet struct {
	Unary  map[term.Atom]func(x term.Interface, env *term.Env) (term.Interface, error)
	Binary map[term.Atom]func(x, y term.Interface, env *term.Env) (term.Interface, error)
}

// Is evaluates expression and unifies the result with result.
func (fs FunctionSet) Is(result, expression term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	v, err := fs.eval(expression, env)
	if err != nil {
		return nondet.Error(err)
	}
	return nondet.Delay(func() *nondet.Promise {
		env := *env
		return Unify(result, v, k, &env)
	})
}

// Equal succeeds iff lhs equals to rhs.
func (fs FunctionSet) Equal(lhs, rhs term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return fs.compare(lhs, rhs, k, func(i term.Integer, j term.Integer) bool {
		return i == j
	}, func(f term.Float, g term.Float) bool {
		return f == g
	}, env)
}

// NotEqual succeeds iff lhs doesn't equal to rhs.
func (fs FunctionSet) NotEqual(lhs, rhs term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return fs.compare(lhs, rhs, k, func(i term.Integer, j term.Integer) bool {
		return i != j
	}, func(f term.Float, g term.Float) bool {
		return f != g
	}, env)
}

// LessThan succeeds iff lhs is less than rhs.
func (fs FunctionSet) LessThan(lhs, rhs term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return fs.compare(lhs, rhs, k, func(i term.Integer, j term.Integer) bool {
		return i < j
	}, func(f term.Float, g term.Float) bool {
		return f < g
	}, env)
}

// GreaterThan succeeds iff lhs is greater than rhs.
func (fs FunctionSet) GreaterThan(lhs, rhs term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return fs.compare(lhs, rhs, k, func(i term.Integer, j term.Integer) bool {
		return i > j
	}, func(f term.Float, g term.Float) bool {
		return f > g
	}, env)
}

// LessThanOrEqual succeeds iff lhs is less than or equal to rhs.
func (fs FunctionSet) LessThanOrEqual(lhs, rhs term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return fs.compare(lhs, rhs, k, func(i term.Integer, j term.Integer) bool {
		return i <= j
	}, func(f term.Float, g term.Float) bool {
		return f <= g
	}, env)
}

// GreaterThanOrEqual succeeds iff lhs is greater than or equal to rhs.
func (fs FunctionSet) GreaterThanOrEqual(lhs, rhs term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	return fs.compare(lhs, rhs, k, func(i term.Integer, j term.Integer) bool {
		return i >= j
	}, func(f term.Float, g term.Float) bool {
		return f >= g
	}, env)
}

func (fs FunctionSet) compare(lhs, rhs term.Interface, k func(term.Env) *nondet.Promise, pi func(term.Integer, term.Integer) bool, pf func(term.Float, term.Float) bool, env *term.Env) *nondet.Promise {
	l, err := fs.eval(lhs, env)
	if err != nil {
		return nondet.Error(err)
	}

	r, err := fs.eval(rhs, env)
	if err != nil {
		return nondet.Error(err)
	}

	switch l := l.(type) {
	case term.Integer:
		switch r := r.(type) {
		case term.Integer:
			if !pi(l, r) {
				return nondet.Bool(false)
			}
			return k(*env)
		case term.Float:
			if !pf(term.Float(l), r) {
				return nondet.Bool(false)
			}
			return k(*env)
		default:
			return nondet.Error(typeErrorEvaluable(r))
		}
	case term.Float:
		switch r := r.(type) {
		case term.Integer:
			if !pf(l, term.Float(r)) {
				return nondet.Bool(false)
			}
			return k(*env)
		case term.Float:
			if !pf(l, r) {
				return nondet.Bool(false)
			}
			return k(*env)
		default:
			return nondet.Error(typeErrorEvaluable(r))
		}
	default:
		return nondet.Error(typeErrorEvaluable(l))
	}
}

func (fs FunctionSet) eval(expression term.Interface, env *term.Env) (_ term.Interface, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				if e.Error() == "runtime error: integer divide by zero" {
					err = evaluationErrorZeroDivisor()
					return
				}
			}
			panic(r)
		}
	}()

	switch t := env.Resolve(expression).(type) {
	case term.Variable:
		return nil, instantiationError(expression)
	case term.Atom:
		return nil, typeErrorEvaluable(expression) // TODO: constants?
	case term.Integer, term.Float:
		return t, nil
	case *term.Compound:
		switch len(t.Args) {
		case 1:
			f, ok := fs.Unary[t.Functor]
			if !ok {
				return nil, typeErrorEvaluable(expression)
			}
			x, err := fs.eval(t.Args[0], env)
			if err != nil {
				return nil, err
			}
			return f(x, env)
		case 2:
			f, ok := fs.Binary[t.Functor]
			if !ok {
				return nil, typeErrorEvaluable(expression)
			}
			x, err := fs.eval(t.Args[0], env)
			if err != nil {
				return nil, err
			}
			y, err := fs.eval(t.Args[1], env)
			if err != nil {
				return nil, err
			}
			return f(x, y, env)
		default:
			return nil, typeErrorEvaluable(expression)
		}
	default:
		return nil, typeErrorEvaluable(expression)
	}
}

// DefaultFunctionSet is a FunctionSet with builtin functions.
var DefaultFunctionSet = FunctionSet{
	Unary: map[term.Atom]func(term.Interface, *term.Env) (term.Interface, error){
		"-":        unaryNumber(func(i int64) int64 { return -1 * i }, func(n float64) float64 { return -1 * n }),
		"abs":      unaryFloat(math.Abs),
		"atan":     unaryFloat(math.Atan),
		"ceiling":  unaryFloat(math.Ceil),
		"cos":      unaryFloat(math.Cos),
		"exp":      unaryFloat(math.Exp),
		"sqrt":     unaryFloat(math.Sqrt),
		"sign":     unaryNumber(sgn, sgnf),
		"float":    unaryFloat(func(n float64) float64 { return n }),
		"floor":    unaryFloat(math.Floor),
		"log":      unaryFloat(math.Log),
		"sin":      unaryFloat(math.Sin),
		"truncate": unaryFloat(math.Trunc),
		"round":    unaryFloat(math.Round),
		"\\":       unaryInteger(func(i int64) int64 { return ^i }),
	},
	Binary: map[term.Atom]func(term.Interface, term.Interface, *term.Env) (term.Interface, error){
		"+":   binaryNumber(func(i, j int64) int64 { return i + j }, func(n, m float64) float64 { return n + m }),
		"-":   binaryNumber(func(i, j int64) int64 { return i - j }, func(n, m float64) float64 { return n - m }),
		"*":   binaryNumber(func(i, j int64) int64 { return i * j }, func(n, m float64) float64 { return n * m }),
		"/":   binaryFloat(func(n float64, m float64) float64 { return n / m }),
		"//":  binaryInteger(func(i, j int64) int64 { return i / j }),
		"rem": binaryInteger(func(i, j int64) int64 { return i % j }),
		"mod": binaryInteger(func(i, j int64) int64 { return (i%j + j) % j }),
		"**":  binaryFloat(math.Pow),
		">>":  binaryInteger(func(i, j int64) int64 { return i >> j }),
		"<<":  binaryInteger(func(i, j int64) int64 { return i << j }),
		"/\\": binaryInteger(func(i, j int64) int64 { return i & j }),
		"\\/": binaryInteger(func(i, j int64) int64 { return i | j }),
	},
}

func sgn(i int64) int64 {
	return i>>63 | int64(uint64(-i)>>63)
}

func sgnf(f float64) float64 {
	switch {
	case f < 0:
		return -1
	case f == 0:
		return 0
	case f > 0:
		return 1
	default: // NaN
		return f
	}
}

func unaryInteger(f func(i int64) int64) func(term.Interface, *term.Env) (term.Interface, error) {
	return func(x term.Interface, env *term.Env) (term.Interface, error) {
		i, ok := env.Resolve(x).(term.Integer)
		if !ok {
			return nil, typeErrorInteger(x)
		}

		return term.Integer(f(int64(i))), nil
	}
}

func binaryInteger(f func(i, j int64) int64) func(term.Interface, term.Interface, *term.Env) (term.Interface, error) {
	return func(x, y term.Interface, env *term.Env) (term.Interface, error) {
		i, ok := env.Resolve(x).(term.Integer)
		if !ok {
			return nil, typeErrorInteger(x)
		}

		j, ok := env.Resolve(y).(term.Integer)
		if !ok {
			return nil, typeErrorInteger(y)
		}

		return term.Integer(f(int64(i), int64(j))), nil
	}
}

func unaryFloat(f func(n float64) float64) func(term.Interface, *term.Env) (term.Interface, error) {
	return func(x term.Interface, env *term.Env) (term.Interface, error) {
		switch x := env.Resolve(x).(type) {
		case term.Integer:
			return term.Float(f(float64(x))), nil
		case term.Float:
			return term.Float(f(float64(x))), nil
		default:
			return nil, typeErrorEvaluable(x)
		}
	}
}

func binaryFloat(f func(n float64, m float64) float64) func(term.Interface, term.Interface, *term.Env) (term.Interface, error) {
	return func(x, y term.Interface, env *term.Env) (term.Interface, error) {
		switch x := env.Resolve(x).(type) {
		case term.Integer:
			switch y := env.Resolve(y).(type) {
			case term.Integer:
				return term.Float(f(float64(x), float64(y))), nil
			case term.Float:
				return term.Float(f(float64(x), float64(y))), nil
			default:
				return nil, typeErrorEvaluable(y)
			}
		case term.Float:
			switch y := env.Resolve(y).(type) {
			case term.Integer:
				return term.Float(f(float64(x), float64(y))), nil
			case term.Float:
				return term.Float(f(float64(x), float64(y))), nil
			default:
				return nil, typeErrorEvaluable(y)
			}
		default:
			return nil, typeErrorEvaluable(x)
		}
	}
}

func unaryNumber(fi func(i int64) int64, ff func(n float64) float64) func(term.Interface, *term.Env) (term.Interface, error) {
	return func(x term.Interface, env *term.Env) (term.Interface, error) {
		switch x := env.Resolve(x).(type) {
		case term.Integer:
			return term.Integer(fi(int64(x))), nil
		case term.Float:
			return term.Float(ff(float64(x))), nil
		default:
			return nil, typeErrorEvaluable(x)
		}
	}
}

func binaryNumber(fi func(i, j int64) int64, ff func(n, m float64) float64) func(term.Interface, term.Interface, *term.Env) (term.Interface, error) {
	return func(x, y term.Interface, env *term.Env) (term.Interface, error) {
		switch x := env.Resolve(x).(type) {
		case term.Integer:
			switch y := env.Resolve(y).(type) {
			case term.Integer:
				return term.Integer(fi(int64(x), int64(y))), nil
			case term.Float:
				return term.Float(ff(float64(x), float64(y))), nil
			default:
				return nil, typeErrorEvaluable(y)
			}
		case term.Float:
			switch y := env.Resolve(y).(type) {
			case term.Integer:
				return term.Float(ff(float64(x), float64(y))), nil
			case term.Float:
				return term.Float(ff(float64(x), float64(y))), nil
			default:
				return nil, typeErrorEvaluable(y)
			}
		default:
			return nil, typeErrorEvaluable(x)
		}
	}
}

// StreamProperty succeeds iff the stream represented by streamOrAlias has the stream property property.
func (vm *VM) StreamProperty(streamOrAlias, property term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	streams := make([]*term.Stream, 0, len(vm.streams))
	switch s := env.Resolve(streamOrAlias).(type) {
	case term.Variable:
		for _, v := range vm.streams {
			streams = append(streams, v)
		}
	case term.Atom: // ISO standard stream_property/2 doesn't take an alias but why not?
		v, ok := vm.streams[s]
		if !ok {
			return nondet.Error(existenceErrorStream(streamOrAlias))
		}
		streams = append(streams, v)
	case *term.Stream:
		streams = append(streams, s)
	default:
		return nondet.Error(domainErrorStreamOrAlias(streamOrAlias))
	}

	switch p := env.Resolve(property).(type) {
	case term.Variable:
		break
	case term.Atom:
		switch p {
		case "input", "output":
			break
		default:
			return nondet.Error(domainErrorStreamProperty(property))
		}
	case *term.Compound:
		if len(p.Args) != 1 {
			return nondet.Error(domainErrorStreamProperty(property))
		}
		arg := p.Args[0]
		switch p.Functor {
		case "file_name", "mode", "alias", "end_of_stream", "eof_action", "reposition":
			switch env.Resolve(arg).(type) {
			case term.Variable, term.Atom:
				break
			default:
				return nondet.Error(typeErrorAtom(arg))
			}
		case "position":
			if len(p.Args) != 1 {
				return nondet.Error(domainErrorStreamProperty(property))
			}
			switch env.Resolve(p.Args[0]).(type) {
			case term.Variable, term.Integer:
				break
			default:
				return nondet.Error(typeErrorAtom(arg))
			}
		default:
			return nondet.Error(domainErrorStreamProperty(property))
		}
	default:
		return nondet.Error(domainErrorStreamProperty(property))
	}

	var ks []func() *nondet.Promise
	for _, s := range streams {
		var properties []term.Interface

		switch s.Mode {
		case term.StreamModeRead:
			properties = append(properties, &term.Compound{Functor: "mode", Args: []term.Interface{term.Atom("read")}})
		case term.StreamModeWrite:
			properties = append(properties, &term.Compound{Functor: "mode", Args: []term.Interface{term.Atom("write")}})
		case term.StreamModeAppend:
			properties = append(properties, &term.Compound{Functor: "mode", Args: []term.Interface{term.Atom("append")}})
		}

		if s.Alias != "" {
			properties = append(properties, &term.Compound{Functor: "alias", Args: []term.Interface{s.Alias}})
		}

		switch s.EofAction {
		case term.EofActionError:
			properties = append(properties, &term.Compound{Functor: "eof_action", Args: []term.Interface{term.Atom("error")}})
		case term.EofActionEOFCode:
			properties = append(properties, &term.Compound{Functor: "eof_action", Args: []term.Interface{term.Atom("eof_code")}})
		case term.EofActionReset:
			properties = append(properties, &term.Compound{Functor: "eof_action", Args: []term.Interface{term.Atom("reset")}})
		}

		if s.Source != nil {
			properties = append(properties, term.Atom("input"))
			if _, ok := s.Source.(*bufio.Reader); ok {
				properties = append(properties, &term.Compound{Functor: "buffer", Args: []term.Interface{term.Atom("true")}})
			} else {
				properties = append(properties, &term.Compound{Functor: "buffer", Args: []term.Interface{term.Atom("false")}})
			}
		}

		if s.Sink != nil {
			properties = append(properties, term.Atom("output"))
			if _, ok := s.Sink.(*bufio.Writer); ok {
				properties = append(properties, &term.Compound{Functor: "buffer", Args: []term.Interface{term.Atom("true")}})
			} else {
				properties = append(properties, &term.Compound{Functor: "buffer", Args: []term.Interface{term.Atom("false")}})
			}
		}

		if f, ok := s.Closer.(*os.File); ok {
			pos, err := f.Seek(0, 1)
			if err != nil {
				return nondet.Error(err)
			}
			if br, ok := s.Source.(*bufio.Reader); ok {
				pos -= int64(br.Buffered())
			}

			fi, err := f.Stat()
			if err != nil {
				return nondet.Error(err)
			}

			eos := "not"
			switch {
			case pos == fi.Size():
				eos = "at"
			case pos > fi.Size():
				eos = "past"
			}

			properties = append(properties,
				&term.Compound{Functor: "file_name", Args: []term.Interface{term.Atom(f.Name())}},
				&term.Compound{Functor: "position", Args: []term.Interface{term.Integer(pos)}},
				&term.Compound{Functor: "end_of_stream", Args: []term.Interface{term.Atom(eos)}},
			)
		}

		if s.Reposition {
			properties = append(properties, &term.Compound{Functor: "reposition", Args: []term.Interface{term.Atom("true")}})
		} else {
			properties = append(properties, &term.Compound{Functor: "reposition", Args: []term.Interface{term.Atom("false")}})
		}

		switch s.StreamType {
		case term.StreamTypeText:
			properties = append(properties, &term.Compound{Functor: "type", Args: []term.Interface{term.Atom("text")}})
		case term.StreamTypeBinary:
			properties = append(properties, &term.Compound{Functor: "type", Args: []term.Interface{term.Atom("false")}})
		}

		for i := range properties {
			p := properties[i]
			ks = append(ks, func() *nondet.Promise {
				env := *env
				return Unify(property, p, k, &env)
			})
		}
	}
	return nondet.Delay(ks...)
}

// SetStreamPosition sets the position property of the stream represented by streamOrAlias.
func (vm *VM) SetStreamPosition(streamOrAlias, position term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	s, err := vm.stream(streamOrAlias, env)
	if err != nil {
		return nondet.Error(err)
	}

	switch p := env.Resolve(position).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(position))
	case term.Integer:
		f, ok := s.Closer.(*os.File)
		if !ok {
			return nondet.Error(permissionError(term.Atom("reposition"), term.Atom("stream"), streamOrAlias, term.Atom(fmt.Sprintf("%s is not a file.", streamOrAlias))))
		}

		if _, err := f.Seek(int64(p), 0); err != nil {
			return nondet.Error(systemError(err))
		}

		if br, ok := s.Source.(*bufio.Reader); ok {
			br.Reset(f)
		}

		return k(*env)
	default:
		return nondet.Error(typeErrorInteger(position))
	}
}

// CharConversion registers a character conversion from inChar to outChar, or remove the conversion if inChar = outChar.
func (vm *VM) CharConversion(inChar, outChar term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch in := env.Resolve(inChar).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(inChar))
	case term.Atom:
		i := []rune(in)
		if len(i) != 1 {
			return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", inChar))))
		}

		switch out := env.Resolve(outChar).(type) {
		case term.Variable:
			return nondet.Error(instantiationError(outChar))
		case term.Atom:
			o := []rune(out)
			if len(o) != 1 {
				return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", outChar))))
			}

			if vm.CharConversions == nil {
				vm.CharConversions = map[rune]rune{}
			}
			if i[0] == o[0] {
				delete(vm.CharConversions, i[0])
				return k(*env)
			}
			vm.CharConversions[i[0]] = o[0]
			return k(*env)
		default:
			return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", outChar))))
		}
	default:
		return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", inChar))))
	}
}

// CurrentCharConversion succeeds iff a conversion from inChar to outChar is defined.
func (vm *VM) CurrentCharConversion(inChar, outChar term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch in := env.Resolve(inChar).(type) {
	case term.Variable:
		break
	case term.Atom:
		i := []rune(in)
		if len(i) != 1 {
			return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", inChar))))
		}
	default:
		return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", inChar))))
	}

	switch out := env.Resolve(outChar).(type) {
	case term.Variable:
		break
	case term.Atom:
		o := []rune(out)
		if len(o) != 1 {
			return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", outChar))))
		}
	default:
		return nondet.Error(representationError(term.Atom("character"), term.Atom(fmt.Sprintf("%s is not a character.", outChar))))
	}

	if c1, ok := env.Resolve(inChar).(term.Atom); ok {
		r := []rune(c1)
		if r, ok := vm.CharConversions[r[0]]; ok {
			return nondet.Delay(func() *nondet.Promise {
				env := *env
				return Unify(outChar, term.Atom(r), k, &env)
			})
		}
		return nondet.Delay(func() *nondet.Promise {
			env := *env
			return Unify(outChar, c1, k, &env)
		})
	}

	pattern := term.Compound{Args: []term.Interface{inChar, outChar}}
	ks := make([]func() *nondet.Promise, 256)
	for i := 0; i < 256; i++ {
		r := rune(i)
		cr, ok := vm.CharConversions[r]
		if !ok {
			cr = r
		}

		ks[i] = func() *nondet.Promise {
			env := *env
			return Unify(&pattern, &term.Compound{Args: []term.Interface{term.Atom(r), term.Atom(cr)}}, k, &env)
		}
	}
	return nondet.Delay(ks...)
}

// SetPrologFlag sets flag to value.
func (vm *VM) SetPrologFlag(flag, value term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch f := env.Resolve(flag).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(flag))
	case term.Atom:
		switch f {
		case "bounded", "max_integer", "min_integer", "integer_rounding_function", "max_arity":
			return nondet.Error(permissionError(term.Atom("modify"), term.Atom("flag"), f, term.Atom(fmt.Sprintf("%s is not modifiable.", f))))
		case "char_conversion":
			switch a := env.Resolve(value).(type) {
			case term.Variable:
				return nondet.Error(instantiationError(value))
			case term.Atom:
				switch a {
				case "on":
					vm.charConvEnabled = true
					return k(*env)
				case "off":
					vm.charConvEnabled = false
					return k(*env)
				default:
					return nondet.Error(domainErrorFlagValue(&term.Compound{
						Functor: "+",
						Args:    []term.Interface{flag, value},
					}))
				}
			default:
				return nondet.Error(domainErrorFlagValue(&term.Compound{
					Functor: "+",
					Args:    []term.Interface{flag, value},
				}))
			}
		case "debug":
			switch a := env.Resolve(value).(type) {
			case term.Variable:
				return nondet.Error(instantiationError(value))
			case term.Atom:
				switch a {
				case "on":
					vm.debug = true
					return k(*env)
				case "off":
					vm.debug = false
					return k(*env)
				default:
					return nondet.Error(domainErrorFlagValue(&term.Compound{
						Functor: "+",
						Args:    []term.Interface{flag, value},
					}))
				}
			default:
				return nondet.Error(domainErrorFlagValue(&term.Compound{
					Functor: "+",
					Args:    []term.Interface{flag, value},
				}))
			}
		case "unknown":
			switch a := env.Resolve(value).(type) {
			case term.Variable:
				return nondet.Error(instantiationError(value))
			case term.Atom:
				switch a {
				case "error":
					vm.unknown = unknownError
					return k(*env)
				case "warning":
					vm.unknown = unknownWarning
					return k(*env)
				case "fail":
					vm.unknown = unknownFail
					return k(*env)
				default:
					return nondet.Error(domainErrorFlagValue(&term.Compound{
						Functor: "+",
						Args:    []term.Interface{flag, value},
					}))
				}
			default:
				return nondet.Error(domainErrorFlagValue(&term.Compound{
					Functor: "+",
					Args:    []term.Interface{flag, value},
				}))
			}
		default:
			return nondet.Error(domainErrorPrologFlag(flag))
		}
	default:
		return nondet.Error(typeErrorAtom(flag))
	}
}

// CurrentPrologFlag succeeds iff flag is set to value.
func (vm *VM) CurrentPrologFlag(flag, value term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch f := env.Resolve(flag).(type) {
	case term.Variable:
		break
	case term.Atom:
		switch f {
		case "bounded", "max_integer", "min_integer", "integer_rounding_function", "char_conversion", "debug", "max_arity", "unknown":
			break
		default:
			return nondet.Error(domainErrorPrologFlag(flag))
		}
	default:
		return nondet.Error(typeErrorAtom(flag))
	}

	pattern := term.Compound{Args: []term.Interface{flag, value}}
	flags := []term.Interface{
		&term.Compound{Args: []term.Interface{term.Atom("bounded"), term.Atom("true")}},
		&term.Compound{Args: []term.Interface{term.Atom("max_integer"), term.Integer(math.MaxInt64)}},
		&term.Compound{Args: []term.Interface{term.Atom("min_integer"), term.Integer(math.MinInt64)}},
		&term.Compound{Args: []term.Interface{term.Atom("integer_rounding_function"), term.Atom("toward_zero")}},
		&term.Compound{Args: []term.Interface{term.Atom("char_conversion"), onOff(vm.charConvEnabled)}},
		&term.Compound{Args: []term.Interface{term.Atom("debug"), onOff(vm.debug)}},
		&term.Compound{Args: []term.Interface{term.Atom("max_arity"), term.Atom("unbounded")}},
		&term.Compound{Args: []term.Interface{term.Atom("unknown"), term.Atom(vm.unknown.String())}},
	}
	ks := make([]func() *nondet.Promise, len(flags))
	for i := range flags {
		f := flags[i]
		ks[i] = func() *nondet.Promise {
			env := *env
			return Unify(&pattern, f, k, &env)
		}
	}
	return nondet.Delay(ks...)
}

func onOff(b bool) term.Atom {
	if b {
		return "on"
	}
	return "off"
}

func (vm *VM) stream(streamOrAlias term.Interface, env *term.Env) (*term.Stream, error) {
	switch s := env.Resolve(streamOrAlias).(type) {
	case term.Variable:
		return nil, instantiationError(streamOrAlias)
	case term.Atom:
		v, ok := vm.streams[s]
		if !ok {
			return nil, existenceErrorStream(streamOrAlias)
		}
		return v, nil
	case *term.Stream:
		return s, nil
	default:
		return nil, domainErrorStreamOrAlias(streamOrAlias)
	}
}

func (vm *VM) Dynamic(pi term.Interface, k func(term.Env) *nondet.Promise, env *term.Env) *nondet.Promise {
	switch p := env.Resolve(pi).(type) {
	case term.Variable:
		return nondet.Error(instantiationError(pi))
	case *term.Compound:
		if p.Functor != "/" || len(p.Args) != 2 {
			return nondet.Error(typeErrorPredicateIndicator(pi))
		}
		switch f := env.Resolve(p.Args[0]).(type) {
		case term.Variable:
			return nondet.Error(instantiationError(pi))
		case term.Atom:
			switch a := env.Resolve(p.Args[1]).(type) {
			case term.Variable:
				return nondet.Error(instantiationError(pi))
			case term.Integer:
				pi := procedureIndicator{name: f, arity: a}
				p, ok := vm.procedures[pi]
				if !ok {
					vm.procedures[pi] = clauses{}
					return k(*env)
				}
				if _, ok := p.(clauses); !ok {
					return nondet.Bool(false)
				}
				return k(*env)
			default:
				return nondet.Error(typeErrorPredicateIndicator(pi))
			}
		default:
			return nondet.Error(typeErrorPredicateIndicator(pi))
		}
	default:
		return nondet.Error(typeErrorPredicateIndicator(pi))
	}
}