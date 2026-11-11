// Package jsonx holds JSON values as jq 1.7 holds them, so that the Go judge prints what the shell judge's jq printed,
// byte for byte. Objects keep their keys in insertion order. A number keeps the form it came in: a literal its
// decNumber canonical text, a computed one its double, printed in jq's dtoa form (number.go). The writer escapes
// strings as jq does and indents as jq's default and -c forms do (write.go); the reader takes what jq takes, but for
// the spellings RFC 8259 refuses (read.go); and Compare is jq's total order (order.go). The standard library only.
package jsonx

import (
	"slices"
	"unicode/utf8"
)

// Kind is a value's kind, in the order jq sorts kinds: null, false, true, number, string, array, object.
type Kind uint8

// The seven kinds.
const (
	KindNull Kind = iota
	KindFalse
	KindTrue
	KindNumber
	KindString
	KindArray
	KindObject
)

// Value is one JSON value. The zero Value is null. A Value is not changed once made, but for an object's members
// through its *Object.
type Value struct {
	kind Kind
	num  Number
	str  string
	arr  []Value
	obj  *Object
}

// Null is the null value.
func Null() Value { return Value{} }

// Bool is true or false.
func Bool(b bool) Value {
	if b {
		return Value{kind: KindTrue}
	}
	return Value{kind: KindFalse}
}

// Num is the number n.
func Num(n Number) Value { return Value{kind: KindNumber, num: n} }

// Float is the computed number f.
func Float(f float64) Value { return Num(Computed(f)) }

// Str is the string s as jq makes one: each unit of bad UTF-8 becomes U+FFFD (Valid).
func Str(s string) Value { return Value{kind: KindString, str: Valid(s)} }

// Arr is the array of vs, which it keeps.
func Arr(vs []Value) Value { return Value{kind: KindArray, arr: vs} }

// Obj is the object o, which it keeps; a nil o is the empty object.
func Obj(o *Object) Value {
	if o == nil {
		o = NewObject()
	}
	return Value{kind: KindObject, obj: o}
}

// Kind is v's kind.
func (v Value) Kind() Kind { return v.kind }

// Type is v's kind by jq's type builtin: null, boolean, number, string, array or object.
func (v Value) Type() string {
	return [...]string{"null", "boolean", "boolean", "number", "string", "array", "object"}[v.kind]
}

// IsNull says v is null.
func (v Value) IsNull() bool { return v.kind == KindNull }

// Bool is v's truth when v is true or false.
func (v Value) Bool() (b, ok bool) {
	return v.kind == KindTrue, v.kind == KindTrue || v.kind == KindFalse
}

// Num is v's number when v is one.
func (v Value) Num() (Number, bool) { return v.num, v.kind == KindNumber }

// Str is v's string when v is one.
func (v Value) Str() (string, bool) { return v.str, v.kind == KindString }

// Arr is v's elements when v is an array. The slice is v's own.
func (v Value) Arr() ([]Value, bool) { return v.arr, v.kind == KindArray }

// Obj is v's object when v is one.
func (v Value) Obj() (*Object, bool) { return v.obj, v.kind == KindObject }

// String is v as jq's tojson gives it, which is -c's form without the newline.
func (v Value) String() string { return string(AppendCompact(nil, v)) }

// Object is a JSON object as jq keeps one: members in the order their keys first came. Setting a key it holds keeps
// the key's place and takes the new value, so {"a":1,"b":2,"a":3} reads as {"a":3,"b":2}, as in jq.
type Object struct {
	keys []string
	vals []Value
	at   map[string]int
}

// NewObject is an empty object.
func NewObject() *Object { return &Object{at: map[string]int{}} }

// Len is the number of members.
func (o *Object) Len() int { return len(o.keys) }

// Keys is the keys in insertion order.
func (o *Object) Keys() []string { return slices.Clone(o.keys) }

// SortedKeys is the keys in jq's order for keys, by bytes.
func (o *Object) SortedKeys() []string {
	k := slices.Clone(o.keys)
	slices.Sort(k)
	return k
}

// Get is the member under k.
func (o *Object) Get(k string) (Value, bool) {
	i, ok := o.at[Valid(k)]
	if !ok {
		return Value{}, false
	}
	return o.vals[i], true
}

// Member is the i-th member in insertion order.
func (o *Object) Member(i int) (string, Value) { return o.keys[i], o.vals[i] }

// Set puts v under k, k first made a string as Str makes one: a new key goes last, a key held keeps its place.
func (o *Object) Set(k string, v Value) {
	k = Valid(k)
	if i, ok := o.at[k]; ok {
		o.vals[i] = v
		return
	}
	o.at[k] = len(o.keys)
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
}

// Valid is s with each unit of bad UTF-8 replaced by U+FFFD, as jq's jvp_utf8_next cuts units (src/jv_unicode.c):
// a lead byte claims its whole sequence, or as much of it as s has left; the sequence ends at the first byte that is
// not a continuation, and the bytes before that byte are one unit; an overlong form, a surrogate or a code point past
// U+10FFFF is one unit; a continuation or a byte that leads nothing (0x80 to 0xC1, 0xF5 to 0xFF) is one unit by
// itself. Seen on jq-1.7: "a\xe2\x82b" gets one U+FFFD, "a\xff\xfeb" two, "a\xf0A" one, the A inside the unit.
func Valid(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	b := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); {
		n, ok := unit(s[i:])
		if ok {
			b = append(b, s[i:i+n]...)
		} else {
			b = utf8.AppendRune(b, utf8.RuneError)
		}
		i += n
	}
	return string(b)
}

// unit is the length of the unit jvp_utf8_next reads at the start of s, which is not empty, and whether it is a
// code point.
func unit(s string) (n int, ok bool) {
	c := s[0]
	switch {
	case c < 0x80:
		return 1, true
	case c < 0xC2 || c > 0xF4: // a continuation, or a byte jq's table gives no length (0xC0, 0xC1, 0xF5 and up)
		return 1, false
	}
	n = 2
	first := rune(0x80)
	cp := rune(c & 0x1F)
	if c >= 0xE0 {
		n, first, cp = 3, 0x800, rune(c&0x0F)
	}
	if c >= 0xF0 {
		n, first, cp = 4, 0x10000, rune(c&0x07)
	}
	if n > len(s) {
		return len(s), false // the string ends inside the sequence: the rest is the unit
	}
	for i := 1; i < n; i++ {
		if s[i]&0xC0 != 0x80 {
			return i, false
		}
		cp = cp<<6 | rune(s[i]&0x3F)
	}
	if cp < first || 0xD800 <= cp && cp <= 0xDFFF || cp > 0x10FFFF {
		return n, false
	}
	return n, true
}
