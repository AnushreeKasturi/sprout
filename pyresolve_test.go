package main

import (
	"slices"
	"testing"
)

func TestPyImports(t *testing.T) {
	got := pyImports([]byte(`import os, app.core as core  # two
from . import views, models as m
from ..util import (
    helper,
    other,  # trailing
)
from pkg.sub import *
`))
	want := []string{".", ".views", ".models", "..util", "..util.helper", "..util.other", "pkg.sub", "os", "app.core"}
	if !slices.Equal(got, want) {
		t.Errorf("pyImports = %v\n want %v", got, want)
	}
}

func TestPythonResolution(t *testing.T) {
	g := graphFor(t, map[string]string{
		// An app rooted in backend/, not at the repository root.
		"backend/pyproject.toml":       "[project]\nname = \"app\"\n",
		"backend/app/__init__.py":      "",
		"backend/app/core/__init__.py": "",
		"backend/app/core/config.py":   "settings = 1\n",
		"backend/app/core/db.py":       "engine = 1\n",
		"backend/app/types.py":         "X = 1\n",
		"backend/app/api.py": `from app.core import db
from app.core.config import settings
from types import SimpleNamespace
`,
		// A script imports its neighbours; pytest does the same for tests.
		"scripts/run.py":     "import helpers\n",
		"scripts/helpers.py": "",
		// A library elsewhere in the repository, found through its manifest.
		"libs/shared/setup.cfg":              "[metadata]\nname = shared\n",
		"libs/shared/src/shared/__init__.py": "",
		"backend/app/uses_shared.py":         "import shared\n",
	})
	wantDeps(t, g, "backend/app/api.py", "backend/app/core/__init__.py", "backend/app/core/db.py", "backend/app/core/config.py")
	wantDeps(t, g, "scripts/run.py", "scripts/helpers.py")
	wantDeps(t, g, "backend/app/uses_shared.py", "libs/shared/src/shared/__init__.py")
}
