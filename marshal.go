package force

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/gravitational/trace"
)

// Marshal marshals expression to its code representation
// without evaluating it (unless some parts of the expression)
// are unquoted using Unquote
func Marshal(node interface{}) *Marshaler {
	return &Marshaler{
		node: node,
	}
}

// Marshaler marshals expression to string
type Marshaler struct {
	node interface{}
}

func (n *Marshaler) Type() interface{} {
	return ""
}

func (n *Marshaler) MarshalCode(ctx ExecutionContext) ([]byte, error) {
	data, err := MarshalCode(ctx, n.node)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return data, nil
}

// Eval returns code representation of the expression
// without evaluating it
func (n *Marshaler) Eval(ctx ExecutionContext) (interface{}, error) {
	data, err := MarshalCode(ctx, n.node)
	if err != nil {
		return "", trace.Wrap(err)
	}
	return string(data), nil
}

// Unquote evaluates the argument first,
// and then returns code representation of the returned result
func Unquote(node Expression) *Unquoter {
	return &Unquoter{
		node: node,
	}
}

// Unquoter unquotes the expression
type Unquoter struct {
	node Expression
}

// Eval evaluates the argument first,
// and then returns code representation of the returned result
func (u *Unquoter) Eval(ctx ExecutionContext) (interface{}, error) {
	return "", trace.BadParameter("unquote can not be evaluated")
}

func (u *Unquoter) Type() interface{} {
	return u.node.Type()
}

// MarshalCode marshals code
func (u *Unquoter) MarshalCode(ctx ExecutionContext) ([]byte, error) {
	out, err := u.node.Eval(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return MarshalCode(ctx, out)
}

// MarshalCode marshals parsed types into representation
// that could be interpreted by Force interpreter
func MarshalCode(ctx ExecutionContext, iface interface{}) ([]byte, error) {
	if iface == nil {
		return nil, trace.BadParameter("nil was a mistake")
	}
	switch val := iface.(type) {
	case bool:
		return []byte(fmt.Sprintf("%t", val)), nil
	case int:
		return []byte(fmt.Sprintf("%d", val)), nil
	case string:
		return []byte(fmt.Sprintf("%q", val)), nil
	case []string:
		call := &FnCall{
			Fn:   Strings,
			Args: make([]interface{}, len(val)),
		}
		for i := range val {
			call.Args[i] = val[i]
		}
		return call.MarshalCode(ctx)
	case CodeMarshaler:
		return val.MarshalCode(ctx)
	case Expression:
		i, err := val.Eval(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		return MarshalCode(ctx, i)
	case []StringVar:
		call := &FnCall{
			Fn:   Strings,
			Args: make([]interface{}, len(val)),
		}
		for i := range val {
			call.Args[i] = val[i]
		}
		return call.MarshalCode(ctx)
	}
	t := reflect.TypeOf(iface)
	switch t.Kind() {
	case reflect.Slice:
		slice := reflect.ValueOf(iface)
		switch t.Elem().Kind() {
		case reflect.Struct:
			buf := &bytes.Buffer{}
			io.WriteString(buf, "[]")
			packageName := StructPackageName(t.Elem())
			if packageName != "" {
				io.WriteString(buf, packageName+".")
			}
			io.WriteString(buf, StructName(t.Elem()))
			io.WriteString(buf, "{")
			for i := 0; i < slice.Len(); i++ {
				if i != 0 {
					io.WriteString(buf, ",")
				}
				data, err := MarshalCode(ctx, slice.Index(i).Interface())
				if err != nil {
					return nil, trace.Wrap(err)
				}
				buf.Write(data)
			}
			io.WriteString(buf, "}")
			return buf.Bytes(), nil
		case reflect.Interface:
			ifacePtr := reflect.New(t.Elem()).Interface()
			switch ifacePtr.(type) {
			case *StringVar:
				call := &FnCall{
					Fn:   Strings,
					Args: make([]interface{}, slice.Len()),
				}
				for i := 0; i < slice.Len(); i++ {
					call.Args[i] = slice.Index(i).Interface()
				}
				return call.MarshalCode(ctx)
			default:
				return nil, trace.NotImplemented("%T is not implemented yet", ifacePtr)
			}
		default:
			return nil, trace.NotImplemented("not implemented")
		}
	case reflect.Struct:
		buf := &bytes.Buffer{}
		packageName := StructPackageName(t)
		if packageName != "" {
			io.WriteString(buf, packageName+".")
		}
		io.WriteString(buf, StructName(t))
		io.WriteString(buf, "{")
		v := reflect.ValueOf(iface)
		fieldCount := 0
		for i := 0; i < v.NumField(); i++ {
			fieldVal := v.Field(i).Interface()
			fieldType := t.Field(i)
			if fieldVal == nil || fieldType.Tag.Get("code") == "-" || fieldType.Name == metadataFieldName {
				continue
			}
			fieldCount++
			if fieldCount > 1 {
				io.WriteString(buf, ",")
			}
			io.WriteString(buf, fieldType.Name)
			io.WriteString(buf, ":")
			data, err := MarshalCode(ctx, fieldVal)
			if err != nil {
				return nil, trace.Wrap(err)
			}
			buf.Write(data)
		}
		io.WriteString(buf, "}")
		return buf.Bytes(), nil
	}
	return nil, trace.BadParameter("don't know how to marshal %T", iface)
}

// SetStruct sets struct from the value
func SetStruct(ctx ExecutionContext, v interface{}) {
	ctx.SetValue(ContextKey(StructName(reflect.TypeOf(v))), v)
}

// StructName returns struct name
func StructName(t reflect.Type) string {
	name := ""
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
		name = t.Name()
	}
	name = t.Name()
	if name != "" {
		return name
	}
	field, ok := t.FieldByName(metadataFieldName)
	if !ok {
		return ""
	}
	return field.Type.Name()
}

// OriginalType is original struct type
func OriginalType(t reflect.Type) reflect.Type {
	field, ok := t.FieldByName(metadataFieldName)
	if !ok {
		return t
	}
	return field.Type
}

// StructPackageName returns originating package name of this struct
func StructPackageName(t reflect.Type) string {
	field, ok := t.FieldByName(metadataFieldName)
	if !ok {
		if t.PkgPath() != reflect.TypeOf(Spec{}).PkgPath() {
			return filepath.Base(t.PkgPath())
		}
		return ""
	}
	return filepath.Base(field.Type.PkgPath())
}

// FunctionName returns function name
func FunctionName(i interface{}) string {
	fullPath := runtime.FuncForPC(reflect.ValueOf(i).Pointer()).Name()
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Ext(fullPath), "."), "-fm")
}

// CodeMarshaler marshals objects
// to code that could be interpreted later
type CodeMarshaler interface {
	// MarshalCode marshals object to text representation
	MarshalCode(ctx ExecutionContext) ([]byte, error)
}

// NewFnCall returns new FnCall instance
func NewFnCall(fn interface{}, args ...interface{}) *FnCall {
	return &FnCall{
		Fn:   fn,
		Args: args,
	}
}

// FnCall is a struct used by marshaler
type FnCall struct {
	// Package is a package name
	Package string
	// Fn is a function, the name will
	// be extracted from it
	Fn interface{}
	// FnName is a function name, will be
	// used instead of Fn if specified
	FnName string
	// Args is a list of arguments to the function
	Args []interface{}
}

// SetExpressionArgs sets arguments from expressions
func (f *FnCall) SetExpressionArgs(expressions []Expression) {
	f.Args = make([]interface{}, len(expressions))
	for i := range expressions {
		f.Args[i] = expressions[i]
	}
}

// MarshalCode marshals object to code
func (f *FnCall) MarshalCode(ctx ExecutionContext) ([]byte, error) {
	buf := &bytes.Buffer{}
	if f.Package != "" && f.Package != "." {
		io.WriteString(buf, f.Package+".")
	}
	if f.FnName == "" {
		io.WriteString(buf, FunctionName(f.Fn))
	} else {
		io.WriteString(buf, f.FnName)
	}
	io.WriteString(buf, "(")
	for i := 0; i < len(f.Args); i++ {
		if i != 0 {
			io.WriteString(buf, ", ")
		}
		data, err := MarshalCode(ctx, f.Args[i])
		if err != nil {
			return nil, trace.Wrap(err)
		}
		buf.Write(data)
	}
	io.WriteString(buf, ")")
	return buf.Bytes(), nil
}
