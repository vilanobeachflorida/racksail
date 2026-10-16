package main

// The core's operations that live beside what they serve add themselves to
// every API server the binary builds.
import (
	_ "github.com/vilanobeachflorida/racksail/internal/hooks/hooksapi"
	_ "github.com/vilanobeachflorida/racksail/internal/modules/modulesapi"
	_ "github.com/vilanobeachflorida/racksail/internal/notify/notifyapi"
	_ "github.com/vilanobeachflorida/racksail/internal/recipes/recipesapi"
)
