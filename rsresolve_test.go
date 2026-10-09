package main

import (
	"slices"
	"testing"
)

func TestRsImports(t *testing.T) {
	src := []byte(`// use crate::commented::Out;
/* outer /* nested use crate::nested; */ still comment */
pub(crate) mod parser;
use crate::{a::{B, c::D as E}, f};
use super::*;
use self::g::{self, H};
fn x<'a>(s: &'a str) -> char { let _ = "use crate::in_string;"; let _ = r#"use crate::raw;"#; '{' }
mod tests {
    use super::helper;
    use super::super::up;
}
`)
	code := stripRustComments(src)
	got := rsImports(code, inlineModules(code))
	want := []string{"parser", "crate::a::B", "crate::a::c::D", "crate::f", "super::*", "self::g", "self::g::H", "self::helper", "super::up"}
	if !slices.Equal(got, want) {
		t.Errorf("rsImports = %v\n want %v", got, want)
	}
}

func TestRustWorkspace(t *testing.T) {
	g := graphFor(t, map[string]string{
		"Cargo.toml":                 "[package]\nname = \"tool\" # the binary\n\n[[bin]]\nname = \"tool\"\npath = \"crates/core/main.rs\"\n\n[[test]]\nname = \"integration\"\npath = \"tests/tests.rs\"\n",
		"crates/core/main.rs":        "mod flags;\nfn main() { flags::parse(); let _ = my_lib::util::run; }\n",
		"crates/core/flags/mod.rs":   "pub mod parse;\npub mod defs;\n",
		"crates/core/flags/parse.rs": "use super::defs::FLAGS;\npub fn parse() {}\n",
		"crates/core/flags/defs.rs":  "pub const FLAGS: u8 = 0;\nfn x() { crate::flags::parse::parse(); }\n",
		"crates/lib/Cargo.toml":      "[package]\nname = \"my-lib\"\n",
		"crates/lib/src/lib.rs":      "pub mod util;\n",
		"crates/lib/src/util.rs":     "use crate::Thing;\npub fn run() {}\n#[cfg(test)]\nmod tests {\n    use super::run;\n}\n",
		"tests/tests.rs":             "mod util;\nmod feature;\n",
		"tests/util.rs":              "pub fn dir() {}\n",
		"tests/feature.rs":           "use crate::util::dir;\n",
	})
	wantDeps(t, g, "crates/core/main.rs", "crates/core/flags/mod.rs", "crates/core/flags/parse.rs", "crates/lib/src/util.rs")
	wantDeps(t, g, "crates/core/flags/parse.rs", "crates/core/flags/defs.rs")
	wantDeps(t, g, "crates/core/flags/defs.rs", "crates/core/flags/parse.rs")
	wantDeps(t, g, "crates/lib/src/util.rs", "crates/lib/src/lib.rs") // super inside mod tests is util.rs itself
	wantDeps(t, g, "tests/tests.rs", "tests/util.rs", "tests/feature.rs")
	wantDeps(t, g, "tests/feature.rs", "tests/util.rs")
}
