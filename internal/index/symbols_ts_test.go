package index

import "testing"

func TestScanTSBasic(t *testing.T) {
	src := []byte(`
export class Foo {
}

export function bar() {}
export const baz = () => {}
`)
	res := scanTS("foo.ts", src)
	if res.kind != "class" {
		t.Fatalf("kind = %q", res.kind)
	}
	if res.typ != "Foo" {
		t.Fatalf("typ = %q", res.typ)
	}
	if len(res.exports) != 2 {
		t.Fatalf("exports = %v", res.exports)
	}
	syms := toSymbolsTS("foo.ts", res)
	if len(syms) != 2 {
		t.Fatalf("symbols = %d", len(syms))
	}
	want := []string{"Foo.bar", "Foo.baz"}
	for i, sym := range syms {
		if sym.Symbol != want[i] {
			t.Fatalf("symbol[%d] = %q, want %q", i, sym.Symbol, want[i])
		}
	}
}
