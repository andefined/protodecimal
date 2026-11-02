package main

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	protowireImport = protogen.GoImportPath("google.golang.org/protobuf/encoding/protowire")
	mathImport      = protogen.GoImportPath("math")
	utf8Import      = protogen.GoImportPath("unicode/utf8")
)

// decoderName is the struct generated for m by generateDecoders.
func decoderName(m *protogen.Message) string {
	return m.GoIdent.GoName + "Decimals"
}

// allMessages returns every message declared in file, nested ones included.
func allMessages(file *protogen.File) []*protogen.Message {
	var out []*protogen.Message
	var walk func([]*protogen.Message)
	walk = func(messages []*protogen.Message) {
		for _, m := range messages {
			if !m.Desc.IsMapEntry() {
				out = append(out, m)
				walk(m.Messages)
			}
		}
	}
	walk(file.Messages)
	return out
}

func isDecimal(f *protogen.Field, cfg config) bool {
	return f.Message != nil && f.Message.Desc.FullName() == cfg.decimalType
}

// decodable reports whether a decoder can represent m exactly. That rules out
// maps, oneofs and proto3 optional (which is a oneof underneath), cyclic types
// and messages from other files. A nil result in seen means m is being
// visited, which is how a cycle shows up.
func decodable(m *protogen.Message, file *protogen.File, cfg config, seen map[*protogen.Message]*bool) bool {
	if result, ok := seen[m]; ok {
		return result != nil && *result
	}
	seen[m] = nil
	ok := true
	names := make(map[string]bool, len(m.Fields))
	for _, f := range m.Fields {
		names[f.GoName] = true
	}
	for _, f := range m.Fields {
		switch {
		case f.Desc.IsMap(), f.Oneof != nil:
			ok = false
		case f.Message == nil:
		case isDecimal(f, cfg):
		case f.Message.Desc.ParentFile() != file.Desc:
			ok = false
		case !decodable(f.Message, file, cfg, seen):
			ok = false
		}
		if f.Message != nil && !f.Desc.IsList() && names["Has"+f.GoName] {
			ok = false
		}
	}
	seen[m] = &ok
	return ok
}

// decodedMessages picks the messages that get a decoder: those holding a
// decimal, directly or below, and every message they need. It returns nil
// when a generated name would collide with something in the file.
func decodedMessages(file *protogen.File, cfg config) []*protogen.Message {
	if cfg.decimalType != defaultDecimalType || cfg.convImport != defaultConvImport ||
		file.Desc.Syntax() != protoreflect.Proto3 {
		return nil
	}
	selected := make(map[*protogen.Message]bool)
	var holdsDecimal func(m *protogen.Message, path map[*protogen.Message]bool) bool
	holdsDecimal = func(m *protogen.Message, path map[*protogen.Message]bool) bool {
		if path[m] {
			return false
		}
		path[m] = true
		defer delete(path, m)
		for _, f := range m.Fields {
			if isDecimal(f, cfg) {
				return true
			}
			if f.Message != nil && f.Message.Desc.ParentFile() == file.Desc && holdsDecimal(f.Message, path) {
				return true
			}
		}
		return false
	}
	var include func(m *protogen.Message)
	include = func(m *protogen.Message) {
		if selected[m] {
			return
		}
		selected[m] = true
		for _, f := range m.Fields {
			if f.Message != nil && !isDecimal(f, cfg) {
				include(f.Message)
			}
		}
	}
	seen := make(map[*protogen.Message]*bool)
	messages := allMessages(file)
	for _, m := range messages {
		if decodable(m, file, cfg, seen) && holdsDecimal(m, make(map[*protogen.Message]bool)) {
			include(m)
		}
	}
	taken := make(map[string]bool)
	for _, m := range messages {
		taken[m.GoIdent.GoName] = true
		for _, e := range m.Enums {
			taken[e.GoIdent.GoName] = true
		}
	}
	for _, e := range file.Enums {
		taken[e.GoIdent.GoName] = true
	}
	var out []*protogen.Message
	for _, m := range messages {
		if selected[m] {
			if taken[decoderName(m)] {
				return nil
			}
			out = append(out, m)
		}
	}
	return out
}

// scalar describes how one non-message field kind is read from the wire. The
// value expression uses v, the result of consume.
type scalar struct {
	goType  string
	wire    string
	consume string
	value   string
}

func scalarOf(g *protogen.GeneratedFile, f *protogen.Field) scalar {
	ident := func(name string) string { return g.QualifiedGoIdent(protowireImport.Ident(name)) }
	varint := func(goType, value string) scalar {
		return scalar{goType, ident("VarintType"), ident("ConsumeVarint"), value}
	}
	fixed32 := func(goType, value string) scalar {
		return scalar{goType, ident("Fixed32Type"), ident("ConsumeFixed32"), value}
	}
	fixed64 := func(goType, value string) scalar {
		return scalar{goType, ident("Fixed64Type"), ident("ConsumeFixed64"), value}
	}
	maxUint32 := g.QualifiedGoIdent(mathImport.Ident("MaxUint32"))
	switch f.Desc.Kind() {
	case protoreflect.BoolKind:
		return varint("bool", ident("DecodeBool")+"(v)")
	case protoreflect.EnumKind:
		name := g.QualifiedGoIdent(f.Enum.GoIdent)
		return varint(name, name+"(int32(v))")
	case protoreflect.Int32Kind:
		return varint("int32", "int32(v)")
	case protoreflect.Int64Kind:
		return varint("int64", "int64(v)")
	case protoreflect.Uint32Kind:
		return varint("uint32", "uint32(v)")
	case protoreflect.Uint64Kind:
		return varint("uint64", "v")
	case protoreflect.Sint32Kind:
		return varint("int32", "int32("+ident("DecodeZigZag")+"(v & "+maxUint32+"))")
	case protoreflect.Sint64Kind:
		return varint("int64", ident("DecodeZigZag")+"(v)")
	case protoreflect.Sfixed32Kind:
		return fixed32("int32", "int32(v)")
	case protoreflect.Fixed32Kind:
		return fixed32("uint32", "v")
	case protoreflect.FloatKind:
		return fixed32("float32", g.QualifiedGoIdent(mathImport.Ident("Float32frombits"))+"(v)")
	case protoreflect.Sfixed64Kind:
		return fixed64("int64", "int64(v)")
	case protoreflect.Fixed64Kind:
		return fixed64("uint64", "v")
	case protoreflect.DoubleKind:
		return fixed64("float64", g.QualifiedGoIdent(mathImport.Ident("Float64frombits"))+"(v)")
	case protoreflect.BytesKind:
		return scalar{goType: "[]byte", wire: ident("BytesType"), consume: ident("ConsumeBytes")}
	default: // string
		return scalar{goType: "string", wire: ident("BytesType"), consume: ident("ConsumeBytes")}
	}
}

func generateDecoders(g *protogen.GeneratedFile, messages []*protogen.Message, cfg config) {
	for _, m := range messages {
		generateDecoderType(g, m, cfg)
		generateDecoderMethod(g, m, cfg)
	}
}

func generateDecoderType(g *protogen.GeneratedFile, m *protogen.Message, cfg config) {
	dec := g.QualifiedGoIdent(shopspringImport.Ident("Decimal"))
	g.P("// ", decoderName(m), " is ", m.GoIdent.GoName, " decoded straight from wire bytes into plain")
	g.P("// values, without allocating protobuf messages. Reuse one across calls to")
	g.P("// Unmarshal to keep its slices.")
	g.P("type ", decoderName(m), " struct {")
	for _, f := range m.Fields {
		var goType string
		switch {
		case isDecimal(f, cfg):
			goType = dec
		case f.Message != nil:
			goType = decoderName(f.Message)
		default:
			goType = scalarOf(g, f).goType
		}
		if f.Desc.IsList() {
			goType = "[]" + goType
		}
		g.P(f.GoName, " ", goType)
		if f.Message != nil && !f.Desc.IsList() {
			g.P("Has", f.GoName, " bool")
		}
	}
	g.P("}")
	g.P()
}

func generateDecoderMethod(g *protogen.GeneratedFile, m *protogen.Message, cfg config) {
	ident := func(name string) string { return g.QualifiedGoIdent(protowireImport.Ident(name)) }
	parseError := ident("ParseError")
	consumeInto := func(target, consume, input string) {
		g.P(target, ", n := ", consume, "(", input, ")")
		g.P("if n < 0 {")
		g.P("return ", parseError, "(n)")
		g.P("}")
	}
	validUTF8 := func() {
		g.P("if !", g.QualifiedGoIdent(utf8Import.Ident("Valid")), "(v) {")
		g.P("return ", g.QualifiedGoIdent(cfg.convImport.Ident("ErrInvalidUTF8")))
		g.P("}")
	}

	g.P("// Unmarshal replaces x with the message encoded in b. It accepts what")
	g.P("// proto.Unmarshal accepts, with two exceptions: decimals outside the limits")
	g.P("// of ", g.QualifiedGoIdent(cfg.convImport.Ident("Validate")), " are rejected, and so are singular message fields that")
	g.P("// appear twice, which proto.Unmarshal would merge. Unknown fields are skipped.")
	g.P("func (x *", decoderName(m), ") Unmarshal(b []byte) error {")
	g.P("*x = ", decoderName(m), "{")
	for _, f := range m.Fields {
		if f.Desc.IsList() {
			g.P(f.GoName, ": x.", f.GoName, "[:0],")
		}
	}
	g.P("}")
	g.P("for len(b) > 0 {")
	g.P("num, typ, n := ", ident("ConsumeTag"), "(b)")
	g.P("if n < 0 {")
	g.P("return ", parseError, "(n)")
	g.P("}")
	g.P("b = b[n:]")
	g.P("switch {")
	for _, f := range m.Fields {
		number := f.Desc.Number()
		bytesType := ident("BytesType")
		switch {
		case f.Message != nil:
			g.P("case num == ", number, " && typ == ", bytesType, ":")
			consumeInto("v", ident("ConsumeBytes"), "b")
			g.P("b = b[n:]")
			generateMessageCase(g, f, cfg)
		case f.Desc.IsList() && f.Desc.IsPacked():
			s := scalarOf(g, f)
			g.P("case num == ", number, " && typ == ", bytesType, ":")
			consumeInto("p", ident("ConsumeBytes"), "b")
			g.P("b = b[n:]")
			g.P("for len(p) > 0 {")
			consumeInto("v", s.consume, "p")
			g.P("p = p[n:]")
			g.P("x.", f.GoName, " = append(x.", f.GoName, ", ", s.value, ")")
			g.P("}")
			g.P("case num == ", number, " && typ == ", s.wire, ":")
			consumeInto("v", s.consume, "b")
			g.P("b = b[n:]")
			g.P("x.", f.GoName, " = append(x.", f.GoName, ", ", s.value, ")")
		default:
			s := scalarOf(g, f)
			g.P("case num == ", number, " && typ == ", s.wire, ":")
			consumeInto("v", s.consume, "b")
			g.P("b = b[n:]")
			value := s.value
			switch f.Desc.Kind() {
			case protoreflect.StringKind:
				validUTF8()
				value = "string(v)"
			case protoreflect.BytesKind:
				value = "append([]byte{}, v...)"
			}
			if f.Desc.IsList() {
				g.P("x.", f.GoName, " = append(x.", f.GoName, ", ", value, ")")
			} else {
				g.P("x.", f.GoName, " = ", value)
			}
		}
	}
	g.P("default:")
	g.P("if num > ", ident("MaxValidNumber"), " {")
	g.P("return ", g.QualifiedGoIdent(cfg.convImport.Ident("ErrInvalidFieldNumber")))
	g.P("}")
	g.P("n := ", ident("ConsumeFieldValue"), "(num, typ, b)")
	g.P("if n < 0 {")
	g.P("return ", parseError, "(n)")
	g.P("}")
	g.P("b = b[n:]")
	g.P("}")
	g.P("}")
	g.P("return nil")
	g.P("}")
	g.P()
}

// generateMessageCase decodes v, the bytes of a Decimal or nested message.
func generateMessageCase(g *protogen.GeneratedFile, f *protogen.Field, cfg config) {
	unmarshal := g.QualifiedGoIdent(cfg.convImport.Ident("Unmarshal"))
	duplicate := g.QualifiedGoIdent(cfg.convImport.Ident("ErrDuplicateField"))
	name := "x." + f.GoName
	if !f.Desc.IsList() {
		g.P("if x.Has", f.GoName, " {")
		g.P("return ", duplicate)
		g.P("}")
		g.P("x.Has", f.GoName, " = true")
		if isDecimal(f, cfg) {
			g.P("d, err := ", unmarshal, "(v)")
			g.P("if err != nil {")
			g.P("return err")
			g.P("}")
			g.P(name, " = d")
			return
		}
		g.P("if err := ", name, ".Unmarshal(v); err != nil {")
		g.P("return err")
		g.P("}")
		return
	}
	if isDecimal(f, cfg) {
		g.P("d, err := ", unmarshal, "(v)")
		g.P("if err != nil {")
		g.P("return err")
		g.P("}")
		g.P(name, " = append(", name, ", d)")
		return
	}
	// Reslice into spare capacity so nested slices are reused too.
	g.P("i := len(", name, ")")
	g.P("if i < cap(", name, ") {")
	g.P(name, " = ", name, "[:i+1]")
	g.P("} else {")
	g.P(name, " = append(", name, ", ", decoderName(f.Message), "{})")
	g.P("}")
	g.P("if err := ", name, "[i].Unmarshal(v); err != nil {")
	g.P("return err")
	g.P("}")
}
