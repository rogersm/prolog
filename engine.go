package prolog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ichiban/prolog/internal"

	"github.com/sirupsen/logrus"
)

const (
	opVoid byte = iota
	opEnter
	opCall
	opExit
	opConst
	opVar
	opFunctor
	opPop
)

// Engine is a Prolog interpreter. The zero value for Engine is a valid interpreter without any builtin predicates.
type Engine struct {
	EngineState
}

// NewEngine creates an Engine with user_input, user_output and builtin predicates. It is encouraged user_input to be
// *bufio.Reader since it enables a wide range of input operations.
func NewEngine(in io.Reader, out io.Writer) (*Engine, error) {
	input := Stream{
		Reader:    in,
		mode:      "read",
		alias:     "user_input",
		eofAction: "error",
		typ:       "text",
	}
	output := Stream{
		Writer:    out,
		mode:      "write",
		alias:     "user_output",
		eofAction: "error",
		typ:       "text",
	}
	e := Engine{
		EngineState: EngineState{
			streams: map[Atom]*Stream{
				"user_input":  &input,
				"user_output": &output,
			},
			input:  &input,
			output: &output,
		},
	}
	e.Register0("!", Cut)
	e.Register0("repeat", Repeat)
	e.Register1("call", e.Call)
	e.Register1("current_predicate", e.CurrentPredicate)
	e.Register1("assertz", e.Assertz)
	e.Register1("asserta", e.Asserta)
	e.Register1("retract", e.Retract)
	e.Register1("abolish", e.Abolish)
	e.Register1("var", TypeVar)
	e.Register1("float", TypeFloat)
	e.Register1("integer", TypeInteger)
	e.Register1("atom", TypeAtom)
	e.Register1("compound", TypeCompound)
	e.Register1("throw", Throw)
	e.Register2("=", Unify)
	e.Register2("unify_with_occurs_check", UnifyWithOccursCheck)
	e.Register2("=..", Univ)
	e.Register2("copy_term", CopyTerm)
	e.Register3("arg", Arg)
	e.Register3("bagof", e.BagOf)
	e.Register3("setof", e.SetOf)
	e.Register3("catch", e.Catch)
	e.Register3("functor", Functor)
	e.Register3("op", e.Op)
	e.Register3("compare", Compare)
	e.Register3("current_op", e.CurrentOp)
	e.Register1("current_input", e.CurrentInput)
	e.Register1("current_output", e.CurrentOutput)
	e.Register1("set_input", e.SetInput)
	e.Register1("set_output", e.SetOutput)
	e.Register4("open", e.Open)
	e.Register2("close", e.Close)
	e.Register1("flush_output", e.FlushOutput)
	e.Register3("write_term", e.WriteTerm)
	e.Register2("char_code", CharCode)
	e.Register2("put_byte", e.PutByte)
	e.Register2("put_code", e.PutCode)
	e.Register3("read_term", e.ReadTerm)
	e.Register2("get_byte", e.GetByte)
	e.Register2("get_code", e.GetCode)
	e.Register2("peek_byte", e.PeekByte)
	e.Register2("peek_code", e.PeekCode)
	e.Register1("halt", e.Halt)
	e.Register2("clause", e.Clause)
	e.Register2("atom_length", AtomLength)
	e.Register3("atom_concat", AtomConcat)
	e.Register5("sub_atom", SubAtom)
	e.Register2("atom_chars", AtomChars)
	e.Register2("atom_codes", AtomCodes)
	e.Register2("number_chars", NumberChars)
	e.Register2("number_codes", NumberCodes)
	e.Register2("is", DefaultFunctionSet.Is)
	e.Register2("=:=", DefaultFunctionSet.Equal)
	e.Register2("=\\=", DefaultFunctionSet.NotEqual)
	e.Register2("<", DefaultFunctionSet.LessThan)
	e.Register2(">", DefaultFunctionSet.GreaterThan)
	e.Register2("=<", DefaultFunctionSet.LessThanOrEqual)
	e.Register2(">=", DefaultFunctionSet.GreaterThanOrEqual)
	e.Register2("stream_property", e.StreamProperty)
	e.Register2("set_stream_position", e.SetStreamPosition)
	e.Register2("char_conversion", e.CharConversion)
	e.Register2("current_char_conversion", e.CurrentCharConversion)
	e.Register2("set_prolog_flag", e.SetPrologFlag)
	e.Register2("current_prolog_flag", e.CurrentPrologFlag)
	err := e.Exec(`
/*
 *  bootstrap script
 */

% operators
:-(op(1200, xfx, :-)).
:-(op(1200, xfx, -->)).
:-(op(1200, fx, :-)).
:-(op(1200, fx, ?-)).
:-(op(1100, xfy, ;)).
:-(op(1050, xfy, ->)).
:-(op(1000, xfy, ,)).
:-(op(900, fy, \+)).
:-(op(700, xfx, =)).
:-(op(700, xfx, \=)).
:-(op(700, xfx, ==)).
:-(op(700, xfx, \==)).
:-(op(700, xfx, @<)).
:-(op(700, xfx, @=<)).
:-(op(700, xfx, @>)).
:-(op(700, xfx, @>=)).
:-(op(700, xfx, is)).
:-(op(700, xfx, =:=)).
:-(op(700, xfx, =\=)).
:-(op(700, xfx, <)).
:-(op(700, xfx, =<)).
:-(op(700, xfx, =\=)).
:-(op(700, xfx, >)).
:-(op(700, xfx, >=)).
:-(op(700, xfx, =..)).
:-(op(500, yfx, +)).
:-(op(500, yfx, -)).
:-(op(500, yfx, /\)).
:-(op(500, yfx, \/)).
:-(op(400, yfx, *)).
:-(op(400, yfx, /)).
:-(op(400, yfx, //)).
:-(op(400, yfx, rem)).
:-(op(400, yfx, mod)).
:-(op(400, yfx, <<)).
:-(op(400, yfx, >>)).
:-(op(200, xfx, **)).
:-(op(200, xfy, ^)).
:-(op(200, fy, \)).
:-(op(200, fy, +)).
:-(op(200, fy, -)).
:-(op(100, xfx, @)).
:-(op(50, xfx, :)).

% conjunction
P, Q :- call(P), call(Q).

% disjunction
P; Q :- call(P).
P; Q :- call(Q).

% true/false
true.
false :- a = b.
fail :- false.

% if then else
If -> Then; Else :- call(If), !, call(Then).
If -> Then; Else :- !, call(Else).
If -> Then :- call(If), !, call(Then).

% logic and control
\+P :- call(P), !, false.
\+P :- true.
once(P) :- call(P), !.

% not unifiable
X \= Y :- \+(X = Y).

% term comparison
X == Y :- compare(=, X, Y).
X \== Y :- \+(X == Y).
X @< Y :- compare(<, X, Y).
X @=< Y :- compare(=, X, Y).
X @=< Y :- compare(<, X, Y).
X @> Y :- compare(>, X, Y).
X @>= Y :- compare(>, X, Y).
X @>= Y :- compare(=, X, Y).

nonvar(X) :- \+var(X).

number(X) :- float(X).
number(X) :- integer(X).

atomic(X) :- nonvar(X), \+compound(X).

findall(Term, Goal, List) :- bagof(Term, _^Goal, List), !.
findall(Term, Goal, []).

open(Filename, Mode, Stream) :- open(Filename, Mode, Stream, []).

close(Stream) :- close(Stream, []).

flush_output :- current_output(S), flush_output(S).

write_term(Term, Options) :- current_output(S), write_term(S, Term, Options).

write(Stream, Term) :- write_term(Stream, Term, [numbervars(true)]).

write(Term) :- current_output(S), write(S, Term).

write_canonical(Stream, Term) :- write_term(Stream, Term, [quoted(true), ignore_ops(true)]).

write_canonical(Term) :- current_output(S), write_canonical(S, Term).

writeq(Stream, Term) :- write_term(Stream, Term, [quoted(true), numbervars(true)]).

writeq(Term) :- current_output(S), writeq(S, Term).

nl(Stream) :- write_term(Stream, '\n', []).

nl :- current_output(S), nl(S).

put_byte(Byte) :- current_output(S), put_byte(S, Byte).

put_code(Code) :- current_output(S), put_code(S, Code).

put_char(Stream, Char) :- char_code(Char, Code), put_code(Stream, Code).

put_char(Char) :- current_output(S), put_char(S, Char).

read_term(Term, Options) :- current_input(S), read_term(S, Term, Options).

read(Stream, Term) :- read_term(Stream, Term, []).

read(Term) :- current_input(S), read(S, Term).

get_byte(Byte) :- current_input(S), get_byte(S, Byte).

get_code(Code) :- current_input(S), get_code(S, Code).

get_char(Stream, Char) :- get_code(Stream, Code), char_code(Char, Code).

get_char(Char) :- current_input(S), get_char(S, Char).

peek_byte(Byte) :- current_input(S), peek_byte(S, Byte).

peek_code(Code) :- current_input(S), peek_code(S, Code).

peek_char(Stream, Char) :- peek_code(Stream, Code), char_code(Char, Code).

peek_char(Char) :- current_input(S), peek_char(S, Char).

halt :- halt(0).

at_end_of_stream(Stream) :- stream_property(Stream, end_of_stream(at)).
at_end_of_stream(Stream) :- stream_property(Stream, end_of_stream(past)).

at_end_of_stream :- current_input(S), at_end_of_stream(S).
`)
	return &e, err
}

// Exec executes a prolog program.
func (e *Engine) Exec(s string) error {
	var conv map[rune]rune
	if e.charConvEnabled {
		conv = e.charConversions
	}
	p := NewParser(bufio.NewReader(strings.NewReader(s)), &e.operators, conv)
	for {
		if _, err := p.accept(internal.TokenEOS); err == nil {
			return nil
		}

		t, err := p.Clause()
		if err != nil {
			return err
		}

		if _, err := Force(e.Assertz(t, Done)); err != nil {
			return err
		}
	}
}

// Describe stringifies the given variable in the format of 'V = f(a, b)'.
func (e *Engine) Describe(v *Variable) string {
	var buf bytes.Buffer
	_ = v.WriteTerm(&buf, WriteTermOptions{Quoted: true, Ops: e.operators, Descriptive: true})
	return buf.String()
}

// Query executes a prolog query and calls the callback function for each solution. Returning true from the callback
// halts the iteration.
func (e *Engine) Query(s string, cb func(vars []*Variable) bool) (bool, error) {
	if cb == nil {
		cb = func([]*Variable) bool { return true }
	}

	var conv map[rune]rune
	if e.charConvEnabled {
		conv = e.charConversions
	}
	t, err := NewParser(bufio.NewReader(strings.NewReader(s)), &e.operators, conv).Clause()
	if err != nil {
		return false, err
	}

	a := newAssignment(t)

	return Force(e.Call(t, func() Promise {
		return Bool(cb(a))
	}))
}

// Register0 registers a predicate of arity 0.
func (e *Engine) Register0(name string, p func(func() Promise) Promise) {
	if e.procedures == nil {
		e.procedures = map[string]procedure{}
	}
	e.procedures[PrincipalFunctor(Atom(name), 0).String()] = predicate0(p)
}

// Register1 registers a predicate of arity 1.
func (e *Engine) Register1(name string, p func(Term, func() Promise) Promise) {
	if e.procedures == nil {
		e.procedures = map[string]procedure{}
	}
	e.procedures[PrincipalFunctor(Atom(name), 1).String()] = predicate1(p)
}

// Register2 registers a predicate of arity 2.
func (e *Engine) Register2(name string, p func(Term, Term, func() Promise) Promise) {
	if e.procedures == nil {
		e.procedures = map[string]procedure{}
	}
	e.procedures[PrincipalFunctor(Atom(name), 2).String()] = predicate2(p)
}

// Register3 registers a predicate of arity 3.
func (e *Engine) Register3(name string, p func(Term, Term, Term, func() Promise) Promise) {
	if e.procedures == nil {
		e.procedures = map[string]procedure{}
	}
	e.procedures[PrincipalFunctor(Atom(name), 3).String()] = predicate3(p)
}

// Register4 registers a predicate of arity 4.
func (e *Engine) Register4(name string, p func(Term, Term, Term, Term, func() Promise) Promise) {
	if e.procedures == nil {
		e.procedures = map[string]procedure{}
	}
	e.procedures[PrincipalFunctor(Atom(name), 4).String()] = predicate4(p)
}

// Register5 registers a predicate of arity 5.
func (e *Engine) Register5(name string, p func(Term, Term, Term, Term, Term, func() Promise) Promise) {
	if e.procedures == nil {
		e.procedures = map[string]procedure{}
	}
	e.procedures[PrincipalFunctor(Atom(name), 5).String()] = predicate5(p)
}

// EngineState is an internal state of Engine, a subject to many builtin predicates.
type EngineState struct {
	// BeforeHalt is a hook which gets triggered right before halt/0 or halt/1.
	BeforeHalt []func()

	operators       Operators
	procedures      map[string]procedure
	streams         map[Atom]*Stream
	input, output   *Stream
	charConversions map[rune]rune
	charConvEnabled bool
	debug           bool
	unknown         unknownAction
}

type unknownAction int

const (
	unknownError unknownAction = iota
	unknownFail
	unknownWarning
)

func (u unknownAction) String() string {
	switch u {
	case unknownError:
		return "error"
	case unknownFail:
		return "fail"
	case unknownWarning:
		return "warning"
	default:
		return fmt.Sprintf("unknown(%d)", u)
	}
}

type procedure interface {
	Call(*EngineState, Term, func() Promise) Promise
}

func (e *EngineState) arrive(name string, args Term, k func() Promise) Promise {
	logrus.WithFields(logrus.Fields{
		"name": name,
		"args": loggableTerm{term: args},
	}).Debug("arrive")
	p := e.procedures[name]
	if p == nil {
		switch e.unknown {
		case unknownError:
			return Error(fmt.Errorf("unknown procedure: %s", name))
		case unknownWarning:
			logrus.WithField("procedure", name).Warn("unknown procedure")
			fallthrough
		case unknownFail:
			return Bool(false)
		default:
			return Error(fmt.Errorf("unknown unknown: %s", e.unknown))
		}
	}
	return Delay(func() Promise {
		return p.Call(e, args, k)
	})
}

type loggableTerm struct {
	term Term
}

func (l loggableTerm) String() string {
	if l.term == nil {
		return "nil"
	}

	opts := defaultWriteTermOptions
	opts.Descriptive = true

	var buf bytes.Buffer
	_ = l.term.WriteTerm(&buf, opts)
	return buf.String()
}

type loggableVars struct {
	vars []*Variable
}

func (l loggableVars) String() string {
	opts := defaultWriteTermOptions
	opts.Descriptive = true

	ret := make([]string, len(l.vars))
	for i, v := range l.vars {
		var buf bytes.Buffer
		_ = v.WriteTerm(&buf, opts)
		ret[i] = buf.String()
	}
	return fmt.Sprint(ret)
}

func (e *EngineState) exec(pc bytecode, xr []Term, vars []*Variable, k func() Promise, args, astack Term) Promise {
	for len(pc) != 0 {
		log := logrus.WithFields(logrus.Fields{
			"xr":     xr,
			"vars":   loggableVars{vars: vars},
			"args":   loggableTerm{term: args},
			"astack": loggableTerm{term: astack},
		})
		switch pc[0] {
		case opVoid:
			log.Debug("void")
			pc = pc[1:]
		case opConst:
			log.Debugf("const %d", pc[1])
			x := xr[pc[1]]
			var arest Variable
			if !args.Unify(Cons(x, &arest), false) {
				return Bool(false)
			}
			pc = pc[2:]
			args = &arest
		case opVar:
			log.Debugf("var %d", pc[1])
			v := vars[pc[1]]
			var arest Variable
			if !args.Unify(Cons(v, &arest), false) {
				return Bool(false)
			}
			pc = pc[2:]
			args = &arest
		case opFunctor:
			log.Debugf("functor %d", pc[1])
			x := xr[pc[1]]
			var arg, arest Variable
			if !args.Unify(Cons(&arg, &arest), false) {
				return Bool(false)
			}
			var fatom, farity Variable
			if !x.Unify(&Compound{
				Functor: "/",
				Args:    []Term{&fatom, &farity},
			}, false) {
				return Bool(false)
			}
			ok, err := Force(Functor(&arg, &fatom, &farity, Done))
			if err != nil {
				return Error(err)
			}
			if !ok {
				return Bool(false)
			}
			pc = pc[2:]
			args = &Variable{}
			ok, err = Force(Univ(&arg, Cons(&fatom, args), Done))
			if err != nil {
				return Error(err)
			}
			if !ok {
				return Bool(false)
			}
			astack = Cons(&arest, astack)
		case opPop:
			log.Debug("pop")
			if !args.Unify(List(), false) {
				return Bool(false)
			}
			pc = pc[1:]
			var a, arest Variable
			if !astack.Unify(Cons(&a, &arest), false) {
				return Bool(false)
			}
			args = &a
			astack = &arest
		case opEnter:
			log.Debug("enter")
			if !args.Unify(List(), false) {
				return Bool(false)
			}
			if !astack.Unify(List(), false) {
				return Bool(false)
			}
			pc = pc[1:]
			var v Variable
			args = &v
			astack = &v
		case opCall:
			log.Debugf("call %d", pc[1])
			x := xr[pc[1]]
			if !args.Unify(List(), false) {
				return Bool(false)
			}
			pc = pc[2:]
			return Delay(func() Promise {
				return e.arrive(x.String(), astack, func() Promise {
					var v Variable
					return Delay(func() Promise {
						return e.exec(pc, xr, vars, k, &v, &v)
					})
				})
			})
		case opExit:
			log.Debug("exit")
			return Delay(k)
		default:
			return Error(fmt.Errorf("unknown(%d)", pc[0]))
		}
	}
	return Error(errors.New("non-exit end of bytecode"))
}

type clauses []clause

func (cs clauses) Call(e *EngineState, args Term, k func() Promise) Promise {
	if len(cs) == 0 {
		return Bool(false)
	}

	log := logrus.WithFields(logrus.Fields{
		"name": cs[0].name,
		"args": args,
	})

	a := newAssignment(args)

	i := 0
	var f func() Promise
	f = func() Promise {
		if i == len(cs) {
			log.Info("fail")
			return Bool(false)
		}
		a.reset()

		c := cs[i]

		log = log.WithField("choice", i)

		switch i {
		case 0:
			log.Info("call")
		default:
			log.Info("redo")
		}

		vars := make([]*Variable, len(c.vars))
		for i := range c.vars {
			vars[i] = &Variable{}
		}
		i++

		return Delay(func() Promise {
			return e.exec(c.bytecode, c.xrTable, vars, k, args, List())
		}, f)
	}

	return Delay(f)
}

type clause struct {
	name     string
	raw      Term
	xrTable  []Term
	vars     []*Variable
	bytecode bytecode
}

func (c *clause) compile(t Term) error {
	t = Resolve(t)
	c.raw = t
	switch t := t.(type) {
	case Atom:
		return c.compileClause(t, nil)
	case *Compound:
		if t.Functor == ":-" {
			return c.compileClause(t.Args[0], t.Args[1])
		}
		return c.compileClause(t, nil)
	default:
		return fmt.Errorf("not a compound: %s", t)
	}
}

func (c *clause) compileClause(head Term, body Term) error {
	switch head := head.(type) {
	case Atom:
	case *Compound:
		for _, a := range head.Args {
			if err := c.compileArg(a); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("not an atom nor compound: %s", head)
	}
	if body != nil {
		c.bytecode = append(c.bytecode, opEnter)
		for {
			p, ok := body.(*Compound)
			if !ok || p.Functor != "," || len(p.Args) != 2 {
				break
			}
			if err := c.compilePred(p.Args[0]); err != nil {
				return err
			}
			body = p.Args[1]
		}
		if err := c.compilePred(body); err != nil {
			return err
		}
	}
	c.bytecode = append(c.bytecode, opExit)
	return nil
}

func (c *clause) compilePred(p Term) error {
	switch p := p.(type) {
	case Atom:
		c.bytecode = append(c.bytecode, opCall, c.xrOffset(&Compound{
			Functor: "/",
			Args:    []Term{p, Integer(0)},
		}))
		return nil
	case *Compound:
		for _, a := range p.Args {
			if err := c.compileArg(a); err != nil {
				return err
			}
		}
		c.bytecode = append(c.bytecode, opCall, c.xrOffset(&Compound{
			Functor: "/",
			Args:    []Term{p.Functor, Integer(len(p.Args))},
		}))
		return nil
	default:
		return errors.New("not a predicate")
	}
}

func (c *clause) compileArg(a Term) error {
	switch a := a.(type) {
	case Atom:
		c.bytecode = append(c.bytecode, opConst, c.xrOffset(a))
	case Integer:
		c.bytecode = append(c.bytecode, opConst, c.xrOffset(a))
	case *Variable:
		c.bytecode = append(c.bytecode, opVar, c.varOffset(a))
	case *Compound:
		c.bytecode = append(c.bytecode, opFunctor, c.xrOffset(&Compound{
			Functor: "/",
			Args:    []Term{a.Functor, Integer(len(a.Args))},
		}))
		for _, n := range a.Args {
			if err := c.compileArg(n); err != nil {
				return err
			}
		}
		c.bytecode = append(c.bytecode, opPop)
	default:
		return errors.New("unknown")
	}
	return nil
}

func (c *clause) xrOffset(o Term) byte {
	for i, r := range c.xrTable {
		if r.Unify(o, false) {
			return byte(i)
		}
	}
	c.xrTable = append(c.xrTable, o)
	return byte(len(c.xrTable) - 1)
}

func (c *clause) varOffset(o *Variable) byte {
	for i, v := range c.vars {
		if v == o {
			return byte(i)
		}
	}
	o.Name = ""
	c.vars = append(c.vars, o)
	return byte(len(c.vars) - 1)
}

type bytecode []byte

func (b bytecode) String() string {
	ret := make([]string, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case opVoid:
			ret = append(ret, "void")
		case opConst:
			i++
			ret = append(ret, fmt.Sprintf("const %d", b[i]))
		case opVar:
			i++
			ret = append(ret, fmt.Sprintf("var %d", b[i]))
		case opFunctor:
			i++
			ret = append(ret, fmt.Sprintf("functor %d", b[i]))
		case opPop:
			ret = append(ret, "pop")
		case opEnter:
			ret = append(ret, "enter")
		case opCall:
			i++
			ret = append(ret, fmt.Sprintf("call %d", b[i]))
		case opExit:
			ret = append(ret, "exit")
		default:
			ret = append(ret, fmt.Sprintf("unknown(%d)", b[i]))
		}
	}
	return strings.Join(ret, "; ")
}

type predicate0 func(func() Promise) Promise

func (p predicate0) Call(e *EngineState, args Term, k func() Promise) Promise {
	if !args.Unify(List(), false) {
		return Error(errors.New("wrong number of arguments"))
	}

	return p(func() Promise {
		return e.exec([]byte{opExit}, nil, nil, k, nil, nil)
	})
}

type predicate1 func(Term, func() Promise) Promise

func (p predicate1) Call(e *EngineState, args Term, k func() Promise) Promise {
	var v1 Variable
	if !args.Unify(List(&v1), false) {
		return Error(fmt.Errorf("wrong number of arguments: %s", args))
	}

	return p(&v1, func() Promise {
		return e.exec([]byte{opExit}, nil, nil, k, nil, nil)
	})
}

type predicate2 func(Term, Term, func() Promise) Promise

func (p predicate2) Call(e *EngineState, args Term, k func() Promise) Promise {
	var v1, v2 Variable
	if !args.Unify(List(&v1, &v2), false) {
		return Error(errors.New("wrong number of arguments"))
	}

	return p(&v1, &v2, func() Promise {
		return e.exec([]byte{opExit}, nil, nil, k, nil, nil)
	})
}

type predicate3 func(Term, Term, Term, func() Promise) Promise

func (p predicate3) Call(e *EngineState, args Term, k func() Promise) Promise {
	var v1, v2, v3 Variable
	if !args.Unify(List(&v1, &v2, &v3), false) {
		return Error(errors.New("wrong number of arguments"))
	}

	return p(&v1, &v2, &v3, func() Promise {
		return e.exec([]byte{opExit}, nil, nil, k, nil, nil)
	})
}

type predicate4 func(Term, Term, Term, Term, func() Promise) Promise

func (p predicate4) Call(e *EngineState, args Term, k func() Promise) Promise {
	var v1, v2, v3, v4 Variable
	if !args.Unify(List(&v1, &v2, &v3, &v4), false) {
		return Error(errors.New("wrong number of arguments"))
	}

	return p(&v1, &v2, &v3, &v4, func() Promise {
		return e.exec([]byte{opExit}, nil, nil, k, nil, nil)
	})
}

type predicate5 func(Term, Term, Term, Term, Term, func() Promise) Promise

func (p predicate5) Call(e *EngineState, args Term, k func() Promise) Promise {
	var v1, v2, v3, v4, v5 Variable
	if !args.Unify(List(&v1, &v2, &v3, &v4, &v5), false) {
		return Error(errors.New("wrong number of arguments"))
	}

	return p(&v1, &v2, &v3, &v4, &v5, func() Promise {
		return e.exec([]byte{opExit}, nil, nil, k, nil, nil)
	})
}

type assignment []*Variable

func newAssignment(ts ...Term) assignment {
	var a assignment
	for _, t := range ts {
		a.add(t)
	}
	return a
}

func (a *assignment) add(t Term) {
	switch t := t.(type) {
	case *Variable:
		if t.Ref != nil {
			a.add(t.Ref)
			return
		}
		for _, v := range *a {
			if v == t {
				return
			}
		}
		*a = append(*a, t)
	case *Compound:
		for _, arg := range t.Args {
			a.add(arg)
		}
	}
}

func (a assignment) reset() {
	logrus.WithField("vars", loggableVars{vars: a}).Debug("reset")
	for _, v := range a {
		v.Ref = nil
	}
}

func (a assignment) contains(v *Variable) bool {
	for _, e := range a {
		if e == v {
			return true
		}
	}
	return false
}

// Done terminates a continuation chain.
func Done() Promise {
	return Bool(true)
}
